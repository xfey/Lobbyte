// Package app owns the client application lifecycle and state orchestration.
package app

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xfey/lobbyte/internal/client/hooks"
	"github.com/xfey/lobbyte/internal/client/identity"
	"github.com/xfey/lobbyte/internal/client/ipc"
	"github.com/xfey/lobbyte/internal/client/network"
	"github.com/xfey/lobbyte/internal/client/tui"
	"github.com/xfey/lobbyte/internal/protocol"
)

const (
	operationTimeout = 750 * time.Millisecond
	maxRetryDelay    = 5 * time.Second
)

// Config contains process-level client configuration and release constants.
type Config struct {
	ProfileDir    string
	RuntimeDir    string
	RelayURL      string
	ClientVersion string
	RulesVersion  string
	RulesURL      string
	PrivacyURL    string
	HomeDir       string
	Executable    string
	Input         io.Reader
}

// Validate rejects ambiguous paths and release configuration before state opens.
func (config Config) Validate() error {
	for name, value := range map[string]string{
		"profile": config.ProfileDir, "runtime": config.RuntimeDir,
		"home": config.HomeDir, "executable": config.Executable,
	} {
		if value == "" || !filepath.IsAbs(value) {
			return fmt.Errorf("client %s path must be absolute", name)
		}
	}
	if config.RelayURL == "" || config.ClientVersion == "" || config.RulesVersion == "" {
		return errors.New("relay URL, client version, and rules version are required")
	}
	if (config.RulesURL == "") != (config.PrivacyURL == "") {
		return errors.New("Community Rules and Privacy Notice URLs must be configured together")
	}
	for _, rawURL := range []string{config.RulesURL, config.PrivacyURL} {
		if rawURL == "" {
			continue
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return errors.New("published admission URLs must use https")
		}
	}
	parsedRelay, err := url.Parse(config.RelayURL)
	if err != nil || parsedRelay.Host == "" || (parsedRelay.Scheme != "ws" && parsedRelay.Scheme != "wss") {
		return errors.New("Relay URL must use ws or wss")
	}
	return nil
}

// Run starts the single-instance TUI and owns all process-local resources.
func Run(ctx context.Context, output io.Writer, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if strings.HasPrefix(config.RelayURL, "ws://") {
		if !isLoopbackRelay(config.RelayURL) || !sameOrNested(config.ProfileDir, config.RuntimeDir) {
			return errors.New("plaintext Relay is limited to an isolated local development profile")
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	localIPC, err := ipc.Start(config.RuntimeDir)
	if err != nil {
		return err
	}
	defer localIPC.Close()
	identityFile, err := identity.NewFile(config.ProfileDir)
	if err != nil {
		return err
	}
	model := newRuntimeModel(ctx, config, identityFile, localIPC)
	options := []tea.ProgramOption{
		tea.WithContext(ctx), tea.WithOutput(output), tea.WithAltScreen(), tea.WithMouseCellMotion(),
	}
	if config.Input != nil {
		options = append(options, tea.WithInput(config.Input))
	}
	program := tea.NewProgram(model, options...)
	_, runErr := program.Run()
	model.close()
	if errors.Is(runErr, tea.ErrProgramKilled) || ctx.Err() != nil {
		return nil
	}
	return runErr
}

type runtimeModel struct {
	view               tui.Model
	ctx                context.Context
	config             Config
	identity           identity.File
	ipc                *ipc.Server
	tracker            PresenceTracker
	session            *network.Session
	deviceID           string
	resume             string
	intentID           string
	intent             bool
	generation         uint64
	retry              int
	transportRetry     bool
	pendingRoomCommand protocol.EventType
	presenceSending    bool
	presenceDirty      bool
	pendingMessageID   string
	pendingMessageText string
}

func newRuntimeModel(ctx context.Context, config Config, identityFile identity.File, localIPC *ipc.Server) *runtimeModel {
	view := tui.NewModel(identity.RulesAccepted(config.ProfileDir, config.RulesVersion))
	view.SetAdmissionLinks(config.RulesURL, config.PrivacyURL)
	return &runtimeModel{
		view: view,
		ctx:  ctx, config: config, identity: identityFile, ipc: localIPC,
		tracker: NewPresenceTracker(),
	}
}

func (model *runtimeModel) Init() tea.Cmd {
	return tea.Batch(waitHook(model.ipc.Events()), model.view.Init())
}

func (model *runtimeModel) View() string { return model.view.View() }

func (model *runtimeModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	commands := make([]tea.Cmd, 0, 4)
	switch value := message.(type) {
	case hookEventMsg:
		presence, generation, deadline, err := model.tracker.Apply(value.event, time.Now())
		if err == nil {
			_ = model.view.SetSelfPresence(presence)
			if !deadline.IsZero() {
				commands = append(commands, expireDoneAt(deadline, generation))
			}
			if model.session != nil && model.intent {
				if command := model.queuePresence(); command != nil {
					commands = append(commands, command)
				}
			}
		}
		commands = append(commands, waitHook(model.ipc.Events()))
	case doneExpiredMsg:
		if presence, changed := model.tracker.ExpireDone(value.generation); changed {
			_ = model.view.SetSelfPresence(presence)
			if model.session != nil && model.intent {
				if command := model.queuePresence(); command != nil {
					commands = append(commands, command)
				}
			}
		}
	case connectResultMsg:
		if value.generation != model.generation || !model.intent {
			if value.session != nil {
				_ = value.session.Close()
			}
			break
		}
		if value.identityCorrupt {
			model.intent = false
			model.view.Reconnecting = false
			model.view.ShowIdentityError()
			break
		}
		if value.err != nil {
			var problem *network.DialError
			if errors.As(value.err, &problem) {
				switch {
				case problem.Code == "already_connected" && model.transportRetry &&
					(model.view.Page == tui.PageWaiting || model.view.Page == tui.PageRoom):
					// The Relay may still be releasing the prior transport. A room keeps
					// its recovery token; a waiting connection has no room to restore.
					if model.view.Page == tui.PageWaiting {
						model.resume = ""
					}
					model.view.Reconnecting = true
					commands = append(commands, model.retryCommand())
					break
				case problem.Code == "resume_failed" && model.transportRetry &&
					model.view.Page == tui.PageWaiting && model.pendingRoomCommand == protocol.EventRoomNext:
					// /next deliberately abandoned the old room token, but the Relay may
					// still be releasing that room's transport.
					model.resume = ""
					model.view.Reconnecting = true
					commands = append(commands, model.retryCommand())
					break
				case problem.Code == "resume_failed" && model.resume != "":
					model.resume = ""
					model.intent = false
					model.view.ReturnHome("Could not restore room.")
					break
				default:
					if model.shouldRetry(value.err) {
						model.view.Reconnecting = true
						commands = append(commands, model.retryCommand())
					} else {
						model.intent = false
						model.pendingRoomCommand = ""
						model.view.Page = tui.PageHome
						model.view.Reconnecting = false
						model.view.Notice = value.err.Error()
					}
				}
				break
			}
			if model.shouldRetry(value.err) {
				model.view.Reconnecting = true
				commands = append(commands, model.retryCommand())
			} else {
				model.intent = false
				model.pendingRoomCommand = ""
				model.view.Page = tui.PageHome
				model.view.Reconnecting = false
				model.view.Notice = value.err.Error()
			}
			break
		}
		model.deviceID, model.session = value.deviceID, value.session
		model.resume = value.session.Ready().ResumeToken
		model.view.BuilderID = value.session.Ready().BuilderID
		model.view.Reconnecting = false
		model.retry = 0
		model.transportRetry = false
		model.presenceSending = false
		model.presenceDirty = false
		commands = append(commands, waitNetwork(value.session))
	case reconnectMsg:
		if value.generation == model.generation && model.intent && model.session == nil {
			commands = append(commands, model.connectCommand())
		}
	case relayEventMsg:
		if value.session != model.session {
			break
		}
		previousPage := model.view.Page
		messageWasPending := model.pendingMessageID != ""
		if value.event.Type == protocol.EventError {
			problem, err := protocol.Payload[protocol.Error](value.event)
			if err == nil {
				err = problem.Validate()
			}
			if err == nil && problem.Code == "block_unconfirmed" {
				model.view.ReturnHome(problem.Message)
				model.stopConnection()
				commands = append(commands, clearNoticeAfter(problem.Message))
				break
			}
			if err == nil && problem.EventID != "" && problem.EventID == model.pendingMessageID {
				model.pendingMessageID = ""
				model.pendingMessageText = ""
				model.view.MessageSendRejected()
			}
			if err == nil && problem.Retryable && model.view.Page == tui.PageRoom && model.pendingRoomCommand == "" {
				// A room remains usable when matching storage is unavailable. Operation
				// errors such as message rejection must not discard its recovery token.
				_ = model.view.ApplyRelay(value.event)
				commands = append(commands, waitNetwork(value.session))
				break
			}
			if err == nil && problem.Retryable && model.view.Page == tui.PageWaiting {
				model.view.Notice = problem.Message
				_ = value.session.Close()
				model.session = nil
				model.resume = ""
				model.transportRetry = true
				model.view.Reconnecting = true
				commands = append(commands, model.retryCommand())
				break
			}
		}
		if value.event.Type == protocol.EventRoomEnded || value.event.Type == protocol.EventRoomJoined ||
			value.event.Type == protocol.EventMatchWaiting {
			model.pendingRoomCommand = ""
		}
		if err := model.view.ApplyRelay(value.event); err != nil {
			model.view.ReturnHome("Relay sent an invalid event.")
			model.stopConnection()
			break
		}
		if value.event.Type == protocol.EventMessageReceived {
			message, _ := protocol.Payload[protocol.MessageReceived](value.event)
			if model.view.Page == tui.PageRoom && message.RoomID == model.view.RoomID {
				if message.SenderID == model.view.BuilderID && messageWasPending &&
					message.Text == model.pendingMessageText {
					model.pendingMessageID = ""
					model.pendingMessageText = ""
				} else if message.SenderID == model.view.BuilderID {
					model.view.ReturnHome("Relay sent an invalid event.")
					model.stopConnection()
					break
				}
			}
		}
		if model.view.Page != tui.PageRoom {
			model.pendingMessageID = ""
			model.pendingMessageText = ""
		}
		if previousPage != tui.PageWaiting && model.view.Page == tui.PageWaiting {
			commands = append(commands, model.view.Spinner.Tick)
		}
		if model.view.Notice != "" {
			commands = append(commands, clearNoticeAfter(model.view.Notice))
		}
		if model.view.Page == tui.PageHome {
			model.stopConnection()
		}
		if model.session == value.session {
			commands = append(commands, waitNetwork(value.session))
		}
	case relayClosedMsg:
		if value.session == model.session {
			if model.view.Page == tui.PageRoom {
				model.view.MessageSendFailed()
			}
			model.pendingMessageID = ""
			model.pendingMessageText = ""
			_ = model.session.Close()
			model.session = nil
			model.presenceSending = false
			model.presenceDirty = false
			if model.pendingRoomCommand == protocol.EventRoomBlock {
				model.pendingRoomCommand = ""
				model.view.ReturnHome("Could not confirm block.")
				model.intent = false
				model.resume = ""
				commands = append(commands, clearNoticeAfter("Could not confirm block."))
				break
			}
			var problem *network.DialError
			if errors.As(value.err, &problem) && !problem.Retryable {
				model.pendingRoomCommand = ""
				model.intent = false
				model.transportRetry = false
				model.resume = ""
				model.view.ReturnHome(problem.Error())
				break
			}
			if model.pendingRoomCommand != protocol.EventRoomNext {
				model.pendingRoomCommand = ""
			}
			if model.intent {
				model.transportRetry = true
				if model.view.Page == tui.PageWaiting {
					model.resume = ""
				}
				model.view.Reconnecting = true
				commands = append(commands, model.retryCommand())
			}
		}
	case sendResultMsg:
		if value.eventType == protocol.EventPresenceSet && value.session == model.session {
			model.presenceSending = false
			if value.err == nil && model.presenceDirty && model.intent {
				model.presenceDirty = false
				if command := model.queuePresence(); command != nil {
					commands = append(commands, command)
				}
			}
		}
		if value.session == model.session && value.err != nil {
			if value.eventType == protocol.EventRoomBlock {
				model.pendingRoomCommand = ""
				model.view.ReturnHome("Could not confirm block.")
				model.stopConnection()
				commands = append(commands, clearNoticeAfter("Could not confirm block."))
				break
			}
			if value.eventType == protocol.EventMessageSend {
				if value.eventID == model.pendingMessageID {
					model.pendingMessageID = ""
					model.pendingMessageText = ""
					model.view.MessageSendFailed()
				}
			}
			commands = append(commands, func() tea.Msg { return relayClosedMsg{session: value.session, err: value.err} })
		}
	case hookResultsMsg:
		results := make([]tui.HookResult, 0, len(value.results))
		for _, result := range value.results {
			name := "Codex"
			if result.Agent == hooks.ClaudeCode {
				name = "Claude Code"
			}
			results = append(results, tui.HookResult{Agent: name, Status: result.Status, Reason: result.Reason})
		}
		model.view.SetHookResults(results)
	case identityReplacedMsg:
		if value.err == nil {
			model.deviceID = value.deviceID
			model.view.Page = tui.PageHome
			model.view.Notice = "New identity created."
		} else {
			model.view.Notice = "Could not create identity."
		}
	case clearNoticeMsg:
		model.view.ClearNotice(value.notice)
	case rulesAcceptedMsg:
		model.view.FinishAdmission(value.err)
	default:
		updated, command := model.view.Update(message)
		model.view = updated.(tui.Model)
		if command != nil {
			commands = append(commands, command)
		}
	}
	for _, effect := range model.view.DrainEffects() {
		commands = append(commands, model.applyEffect(effect)...)
	}
	return model, tea.Batch(commands...)
}

func (model *runtimeModel) applyEffect(effect tui.Command) []tea.Cmd {
	switch effect.Action {
	case "accept-rules":
		return []tea.Cmd{func() tea.Msg {
			return rulesAcceptedMsg{err: identity.AcceptRules(model.config.ProfileDir, model.config.RulesVersion)}
		}}
	case "connect-and-match":
		intentID, err := protocol.NewEventID()
		if err != nil {
			model.view.ReturnHome("Could not start matching.")
			return nil
		}
		model.intent = true
		model.intentID = intentID
		model.resume = ""
		model.generation++
		return []tea.Cmd{model.connectCommand()}
	case "setup-hooks":
		model.view.HooksChecked = false
		manager := hooks.Manager{
			Home: model.config.HomeDir, Executable: model.config.Executable, RuntimeDir: model.config.RuntimeDir,
		}
		return []tea.Cmd{model.view.Spinner.Tick, func() tea.Msg { return hookResultsMsg{results: manager.SetupAll()} }}
	case "replace-identity":
		return []tea.Cmd{func() tea.Msg {
			value, err := model.identity.BackupAndCreateNew()
			return identityReplacedMsg{deviceID: value.DeviceID, err: err}
		}}
	case "quit":
		return []tea.Cmd{tea.Quit}
	}
	switch effect.Type {
	case protocol.EventMatchCancel:
		model.bestEffortSend(protocol.EventMatchCancel, struct{}{})
		model.stopConnection()
	case protocol.EventRoomExit:
		model.bestEffortSend(effect.Type, effect.Payload)
		model.stopConnection()
	case protocol.EventRoomNext:
		model.pendingMessageID = ""
		model.pendingMessageText = ""
		model.resume = ""
		model.pendingRoomCommand = effect.Type
		if model.session != nil {
			return []tea.Cmd{model.view.Spinner.Tick, sendEvent(model.session, effect.Type, effect.Payload)}
		}
		// A reconnect command may already be dialing with a captured room token.
		// Invalidate it so an explicit /next cannot recover the room the user has
		// just left, then start a tokenless retry for the same matching intent.
		model.generation++
		model.transportRetry = true
		model.view.Reconnecting = true
		return []tea.Cmd{model.view.Spinner.Tick, model.connectCommand()}
	case protocol.EventRoomBlock:
		model.pendingMessageID = ""
		model.pendingMessageText = ""
		model.resume = ""
		if model.session == nil {
			model.view.ReturnHome("Could not confirm block.")
			model.stopConnection()
			return []tea.Cmd{clearNoticeAfter("Could not confirm block.")}
		}
		model.pendingRoomCommand = effect.Type
		return []tea.Cmd{model.view.Spinner.Tick, sendEvent(model.session, effect.Type, effect.Payload)}
	case protocol.EventMessageSend:
		if model.session == nil {
			model.view.MessageSendFailed()
			return nil
		}
		eventID, err := protocol.NewEventID()
		if err != nil {
			model.view.MessageSendFailed()
			return nil
		}
		message, ok := effect.Payload.(protocol.MessageSend)
		if !ok {
			model.view.MessageSendFailed()
			return nil
		}
		model.pendingMessageID = eventID
		model.pendingMessageText = message.Text
		return []tea.Cmd{sendEventWithID(model.session, effect.Type, eventID, effect.Payload)}
	}
	return nil
}

func (model *runtimeModel) connectCommand() tea.Cmd {
	generation := model.generation
	deviceID, resume := model.deviceID, model.resume
	presence := model.tracker.Presence()
	return func() tea.Msg {
		if deviceID == "" {
			value, err := model.identity.LoadOrCreate()
			if err != nil {
				return connectResultMsg{generation: generation, err: err, identityCorrupt: true}
			}
			deviceID = value.DeviceID
		}
		dialContext, cancelDial := context.WithTimeout(model.ctx, 5*time.Second)
		defer cancelDial()
		session, err := network.Dial(dialContext, network.Config{
			URL: model.config.RelayURL, DeviceID: deviceID, ClientVersion: model.config.ClientVersion,
			RulesVersion: model.config.RulesVersion, ResumeToken: resume,
		})
		if err != nil {
			return connectResultMsg{generation: generation, deviceID: deviceID, err: err}
		}
		operationContext, cancel := context.WithTimeout(model.ctx, operationTimeout)
		defer cancel()
		if _, err = session.Send(operationContext, protocol.EventPresenceSet, presence); err == nil && !session.Ready().Recovered {
			_, err = session.Send(operationContext, protocol.EventMatchJoin, protocol.MatchJoin{IntentID: model.intentID})
		}
		if err != nil {
			_ = session.Close()
			return connectResultMsg{generation: generation, deviceID: deviceID, err: err}
		}
		return connectResultMsg{generation: generation, deviceID: deviceID, session: session}
	}
}

func (model *runtimeModel) shouldRetry(err error) bool {
	var problem *network.DialError
	if errors.As(err, &problem) {
		return problem.Retryable
	}
	return true
}

func (model *runtimeModel) retryCommand() tea.Cmd {
	generation := model.generation
	delay := time.Duration(1<<min(model.retry, 3)) * 250 * time.Millisecond
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	delay += retryJitter(delay / 4)
	model.retry++
	return tea.Tick(delay, func(time.Time) tea.Msg { return reconnectMsg{generation: generation} })
}

func retryJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return 0
	}
	return time.Duration(binary.LittleEndian.Uint64(value[:]) % uint64(limit))
}

func (model *runtimeModel) stopConnection() {
	model.intent = false
	model.intentID = ""
	model.pendingRoomCommand = ""
	model.transportRetry = false
	model.presenceSending = false
	model.presenceDirty = false
	model.pendingMessageID = ""
	model.pendingMessageText = ""
	model.generation++
	model.resume = ""
	model.view.Reconnecting = false
	if model.session != nil {
		_ = model.session.Close()
		model.session = nil
	}
}

func (model *runtimeModel) bestEffortExit() {
	if model.session != nil && model.view.RoomID != "" {
		model.bestEffortSend(protocol.EventRoomExit, protocol.RoomEvent{RoomID: model.view.RoomID})
	}
}

func (model *runtimeModel) bestEffortSend(eventType protocol.EventType, payload any) {
	if model.session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	_, _ = model.session.Send(ctx, eventType, payload)
}

func (model *runtimeModel) close() {
	model.bestEffortExit()
	if model.session != nil && model.view.Page == tui.PageWaiting {
		model.bestEffortSend(protocol.EventMatchCancel, struct{}{})
	}
	model.stopConnection()
}

type hookEventMsg struct{ event ipc.Event }
type doneExpiredMsg struct{ generation uint64 }
type reconnectMsg struct{ generation uint64 }
type connectResultMsg struct {
	generation      uint64
	deviceID        string
	session         *network.Session
	err             error
	identityCorrupt bool
}
type relayEventMsg struct {
	session *network.Session
	event   protocol.Envelope
}
type relayClosedMsg struct {
	session *network.Session
	err     error
}
type sendResultMsg struct {
	session   *network.Session
	eventType protocol.EventType
	eventID   string
	err       error
}
type hookResultsMsg struct{ results []hooks.Result }
type identityReplacedMsg struct {
	deviceID string
	err      error
}
type clearNoticeMsg struct{ notice string }
type rulesAcceptedMsg struct{ err error }

func waitHook(events <-chan ipc.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return nil
		}
		return hookEventMsg{event: event}
	}
}

func expireDoneAt(deadline time.Time, generation uint64) tea.Cmd {
	return tea.Tick(time.Until(deadline), func(time.Time) tea.Msg { return doneExpiredMsg{generation: generation} })
}

func clearNoticeAfter(notice string) tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return clearNoticeMsg{notice: notice} })
}

func waitNetwork(session *network.Session) tea.Cmd {
	return func() tea.Msg {
		select {
		case event, ok := <-session.Events():
			if !ok {
				return relayClosedMsg{session: session}
			}
			return relayEventMsg{session: session, event: event}
		default:
		}
		select {
		case event, ok := <-session.Events():
			if !ok {
				return relayClosedMsg{session: session}
			}
			return relayEventMsg{session: session, event: event}
		case err, ok := <-session.Errors():
			// readLoop closes Errors before Events. Drain one already queued
			// event first so a final room transition cannot be lost to the
			// channel-close race.
			select {
			case event, eventOK := <-session.Events():
				if eventOK {
					return relayEventMsg{session: session, event: event}
				}
			default:
			}
			if !ok {
				return relayClosedMsg{session: session}
			}
			return relayClosedMsg{session: session, err: err}
		}
	}
}

func sendEvent(session *network.Session, eventType protocol.EventType, payload any) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		eventID, err := session.Send(ctx, eventType, payload)
		return sendResultMsg{session: session, eventType: eventType, eventID: eventID, err: err}
	}
}

func sendEventWithID(session *network.Session, eventType protocol.EventType, eventID string, payload any) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		err := session.SendWithID(ctx, eventType, eventID, payload)
		return sendResultMsg{session: session, eventType: eventType, eventID: eventID, err: err}
	}
}

func (model *runtimeModel) queuePresence() tea.Cmd {
	if model.session == nil || !model.intent {
		return nil
	}
	if model.presenceSending {
		model.presenceDirty = true
		return nil
	}
	model.presenceSending = true
	model.presenceDirty = false
	return sendEvent(model.session, protocol.EventPresenceSet, model.tracker.Presence())
}

func min(first, second int) int {
	if first < second {
		return first
	}
	return second
}

func isLoopbackRelay(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "ws" {
		return false
	}
	host := parsed.Hostname()
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

func sameOrNested(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && relative != ".." && relative != "" && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

var _ tea.Model = (*runtimeModel)(nil)

// Ensure the default executable path passed by cmd remains resolvable after a
// launcher changes its working directory.
func ResolveExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
