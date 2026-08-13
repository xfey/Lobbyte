// Package tui owns the Bubble Tea views and terminal interaction.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/xfey/lobbyte/internal/protocol"
	"github.com/xfey/lobbyte/internal/textcheck"
)

const (
	MinWidth        = 48
	MinHeight       = 16
	MaxRoomMessages = 1000
	MaxRoomBytes    = 1024 * 1024
)

// Page identifies one top-level client view.
type Page int

const (
	PageAdmission Page = iota
	PageHome
	PageWaiting
	PageRoom
	PageHooks
	PageIdentityError
)

// Command is one side effect requested by the pure TUI model.
type Command struct {
	Type    protocol.EventType
	Payload any
	Action  string
}

// Message is one in-memory room message.
type Message struct {
	SenderID int64
	Text     string
}

// HookResult is the minimal presentation form of one Agent setup result.
type HookResult struct {
	Agent  string
	Status string
	Reason string
}

// Model contains only presentation and user interaction state.
type Model struct {
	Page            Page
	Width           int
	Height          int
	AdmissionDone   bool
	RulesURL        string
	PrivacyURL      string
	AdmissionFocus  int
	HomeFocus       int
	IdentityFocus   int
	SelfPresence    protocol.Presence
	PeerPresence    protocol.Presence
	PeerBuilderID   int64
	RoomID          string
	BuilderID       int64
	Reconnecting    bool
	Notice          string
	InputError      string
	Messages        []Message
	messageBytes    int
	trimmed         bool
	sendRate        textcheck.Rate
	messagePending  bool
	pendingText     string
	pendingRevision uint64
	inputRevision   uint64
	Input           textarea.Model
	Viewport        viewport.Model
	Spinner         spinner.Model
	CommandOpen     bool
	CommandFocus    int
	HookResults     []HookResult
	HooksChecked    bool
	Effects         []Command
}

// NewModel creates an admission-first English TUI model.
func NewModel(admissionDone bool) Model {
	input := textarea.New()
	input.Placeholder = "Press / for commands"
	input.ShowLineNumbers = false
	input.CharLimit = textcheck.MaxInputBytes
	input.MaxHeight = textcheck.MaxLines
	input.SetHeight(1)
	input.Prompt = "› "
	configureInputStyles(&input)
	waitingSpinner := spinner.New()
	waitingSpinner.Spinner = spinner.Dot
	page := PageAdmission
	if admissionDone {
		page = PageHome
	}
	return Model{
		Page: page, AdmissionDone: admissionDone,
		SelfPresence: protocol.Presence{State: protocol.AgentIdle},
		PeerPresence: protocol.Presence{State: protocol.AgentIdle},
		Input:        input, Viewport: viewport.New(0, 0), Spinner: waitingSpinner,
	}
}

// configureInputStyles keeps every textarea cell on the same surface. The
// component's default cursor-line background otherwise cuts a terminal-colored
// stripe through the middle of the three-line input area.
func configureInputStyles(input *textarea.Model) {
	background := styles.inputSurface.GetBackground()
	base := lipgloss.NewStyle().Background(background)
	text := base
	muted := styles.muted.Background(background)
	for _, style := range []*textarea.Style{&input.FocusedStyle, &input.BlurredStyle} {
		style.Base = base
		style.CursorLine = text
		style.CursorLineNumber = muted
		style.EndOfBuffer = base
		style.LineNumber = muted
		style.Placeholder = muted
		style.Prompt = muted
		style.Text = text
	}
	input.Blur()
}

// Init satisfies tea.Model. The waiting spinner starts only after the user
// explicitly begins matching.
func (model Model) Init() tea.Cmd { return nil }

// DrainEffects returns pending side effects and clears them from the model.
func (model *Model) DrainEffects() []Command {
	effects := append([]Command(nil), model.Effects...)
	model.Effects = nil
	return effects
}

// Update applies terminal input and window changes without performing I/O.
func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.WindowSizeMsg:
		model.resize(value.Width, value.Height)
		return model, nil
	case tea.MouseMsg:
		if model.Width >= MinWidth && model.Height >= MinHeight && model.Page == PageRoom {
			model.Viewport, _ = model.Viewport.Update(value)
		}
		return model, nil
	case spinner.TickMsg:
		if model.Page != PageWaiting && (model.Page != PageHooks || model.HooksChecked) {
			return model, nil
		}
		var command tea.Cmd
		model.Spinner, command = model.Spinner.Update(value)
		return model, command
	case tea.KeyMsg:
		return model.updateKey(value)
	}
	return model, nil
}

func (model Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if model.Width < MinWidth || model.Height < MinHeight {
		if key.String() == "ctrl+c" {
			model.Effects = append(model.Effects, Command{Action: "quit"})
		}
		return model, nil
	}
	if key.String() == "ctrl+c" {
		model.Effects = append(model.Effects, Command{Action: "quit"})
		return model, nil
	}
	switch model.Page {
	case PageAdmission:
		switch key.String() {
		case "left", "up", "shift+tab":
			model.AdmissionFocus = 0
		case "right", "down", "tab":
			model.AdmissionFocus = 1
		case "enter":
			if model.AdmissionFocus == 0 {
				model.Effects = append(model.Effects, Command{Action: "accept-rules"})
			} else {
				model.Effects = append(model.Effects, Command{Action: "quit"})
			}
		case "esc", "q", "ctrl+c":
			model.Effects = append(model.Effects, Command{Action: "quit"})
		}
	case PageHome:
		return model.updateHome(key)
	case PageWaiting:
		switch key.String() {
		case "esc", "enter", "q":
			model.Page = PageHome
			model.Effects = append(model.Effects, Command{Type: protocol.EventMatchCancel})
		}
	case PageRoom:
		model = model.updateRoom(key)
	case PageHooks:
		if key.String() == "esc" || key.String() == "enter" {
			model.Page = PageHome
		}
	case PageIdentityError:
		switch key.String() {
		case "left", "up", "shift+tab":
			model.IdentityFocus = 0
		case "right", "down", "tab":
			model.IdentityFocus = 1
		case "enter":
			if model.IdentityFocus == 0 {
				model.Effects = append(model.Effects, Command{Action: "replace-identity"})
			} else {
				model.Effects = append(model.Effects, Command{Action: "quit"})
			}
		case "ctrl+c", "q":
			model.Effects = append(model.Effects, Command{Action: "quit"})
		}
	}
	return model, nil
}

func (model Model) updateHome(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k":
		model.HomeFocus = 0
	case "down", "j":
		if model.HomeFocus == 0 {
			model.HomeFocus = 1
		}
	case "left", "shift+tab":
		if model.HomeFocus > 0 {
			model.HomeFocus = 1
		}
	case "right", "tab":
		if model.HomeFocus > 0 {
			model.HomeFocus = 2
		}
	case "enter":
		switch model.HomeFocus {
		case 0:
			model.Page = PageWaiting
			model.Effects = append(model.Effects, Command{Action: "connect-and-match"})
			return model, model.Spinner.Tick
		case 1:
			model.Page = PageHooks
			model.Effects = append(model.Effects, Command{Action: "setup-hooks"})
		case 2:
			model.Effects = append(model.Effects, Command{Action: "quit"})
		}
	case "ctrl+c", "q":
		model.Effects = append(model.Effects, Command{Action: "quit"})
	}
	return model, nil
}

func (model Model) updateRoom(key tea.KeyMsg) Model {
	if key.Paste {
		pasted := string(key.Runes)
		if err := textcheck.ValidateCharacters(pasted); err != nil {
			model.InputError = "Unsupported text"
			return model
		}
		if len(model.Input.Value())+len(pasted) > textcheck.MaxInputBytes {
			model.InputError = "Input is too large"
			return model
		}
		if strings.Count(model.Input.Value(), "\n")+strings.Count(pasted, "\n")+1 > textcheck.MaxLines {
			model.InputError = "Too many lines"
			return model
		}
		model.Input.InsertString(pasted)
		model.inputRevision++
		model.InputError = ""
		if strings.HasPrefix(model.Input.Value(), "/") {
			model.CommandOpen = true
			model.CommandFocus = 0
		} else {
			model.CommandOpen = false
		}
		return model
	}
	if model.CommandOpen {
		options := model.filteredCommands()
		switch key.String() {
		case "esc":
			model.CommandOpen = false
			return model
		case "up":
			if len(options) > 0 {
				model.CommandFocus = (model.CommandFocus + len(options) - 1) % len(options)
			}
			return model
		case "down":
			if len(options) > 0 {
				model.CommandFocus = (model.CommandFocus + 1) % len(options)
			}
			return model
		case "tab":
			if len(options) > 0 {
				model.Input.SetValue(options[model.CommandFocus])
				model.inputRevision++
				model.CommandOpen = false
			}
			return model
		case "enter":
			// Enter never expands a partial command. Tab is the explicit
			// completion action; only complete input may execute.
			return model.submitInput()
		}
	}
	if !model.CommandOpen {
		switch key.String() {
		case "up", "down", "pgup", "pgdown":
			model.Viewport, _ = model.Viewport.Update(key)
			return model
		}
	}
	if key.String() == "enter" {
		return model.submitInput()
	}
	if key.String() == "/" && model.Input.Value() == "" {
		model.CommandOpen = true
	}
	if key.Type == tea.KeyRunes && textcheck.ValidateCharacters(string(key.Runes)) != nil {
		model.InputError = "Unsupported text"
		return model
	}
	before := model.Input.Value()
	updated, _ := model.Input.Update(key)
	if len(updated.Value()) > textcheck.MaxInputBytes {
		updated.SetValue(before)
		model.InputError = "Input is too large"
	}
	model.Input = updated
	if model.Input.Value() != before {
		model.inputRevision++
		model.InputError = ""
	}
	if strings.HasPrefix(model.Input.Value(), "/") {
		model.CommandOpen = true
		options := model.filteredCommands()
		if model.CommandFocus >= len(options) {
			model.CommandFocus = 0
		}
	} else {
		model.CommandOpen = false
	}
	return model
}

var commandNames = []string{"/next", "/exit", "/block"}

func (model Model) filteredCommands() []string {
	prefix := model.Input.Value()
	result := make([]string, 0, len(commandNames))
	for _, command := range commandNames {
		if strings.HasPrefix(command, prefix) {
			result = append(result, command)
		}
	}
	return result
}

func (model Model) submitInput() Model {
	value := model.Input.Value()
	if strings.HasPrefix(value, "/") {
		var eventType protocol.EventType
		switch value {
		case "/next":
			eventType = protocol.EventRoomNext
		case "/exit":
			eventType = protocol.EventRoomExit
		case "/block":
			eventType = protocol.EventRoomBlock
		default:
			model.InputError = "Unknown command"
			return model
		}
		model.Effects = append(model.Effects, Command{Type: eventType, Payload: protocol.RoomEvent{RoomID: model.RoomID}})
		model.clearRoom()
		if eventType == protocol.EventRoomExit {
			model.Page = PageHome
		} else {
			model.Page = PageWaiting
		}
		return model
	}
	if model.messagePending {
		model.InputError = "Message is sending"
		return model
	}
	if err := textcheck.ValidateMessage(value); err != nil {
		model.InputError = shortValidationError(err)
		return model
	}
	if err := model.sendRate.Check(time.Now()); err != nil {
		model.InputError = "Wait a moment before sending"
		return model
	}
	model.Effects = append(model.Effects, Command{
		Type: protocol.EventMessageSend, Payload: protocol.MessageSend{RoomID: model.RoomID, Text: value},
	})
	model.messagePending = true
	model.pendingText = value
	model.pendingRevision = model.inputRevision
	model.InputError = ""
	return model
}

// ApplyRelay updates presentation state from one validated Relay event.
func (model *Model) ApplyRelay(envelope protocol.Envelope) error {
	switch envelope.Type {
	case protocol.EventRoomJoined:
		joined, err := protocol.Payload[protocol.RoomJoined](envelope)
		if err != nil {
			return err
		}
		if joined.RoomID == "" || joined.BuilderID <= 0 {
			return fmt.Errorf("invalid room joined event")
		}
		if joined.BuilderID == model.BuilderID {
			return fmt.Errorf("room peer cannot be the local Builder")
		}
		if err := joined.Presence.Validate(); err != nil {
			return err
		}
		switch {
		case model.Page == PageWaiting && !joined.Recovered:
		case model.Page == PageRoom && joined.Recovered && joined.RoomID == model.RoomID &&
			joined.BuilderID == model.PeerBuilderID:
		default:
			return fmt.Errorf("room joined event is invalid for the current page")
		}
		model.Page, model.RoomID = PageRoom, joined.RoomID
		model.PeerBuilderID, model.PeerPresence = joined.BuilderID, joined.Presence
		model.Input.Focus()
		model.refreshViewport()
	case protocol.EventMatchWaiting:
		if model.Page != PageWaiting {
			return fmt.Errorf("matching confirmation received outside waiting")
		}
		if _, err := protocol.Payload[struct{}](envelope); err != nil {
			return err
		}
	case protocol.EventPresenceChanged:
		changed, err := protocol.Payload[protocol.PresenceChanged](envelope)
		if err != nil {
			return err
		}
		if changed.RoomID != model.RoomID {
			return nil
		}
		if model.Page != PageRoom {
			return fmt.Errorf("presence event received outside a room")
		}
		if err := changed.Presence.Validate(); err != nil {
			return err
		}
		model.PeerPresence = changed.Presence
	case protocol.EventMessageReceived:
		message, err := protocol.Payload[protocol.MessageReceived](envelope)
		if err != nil {
			return err
		}
		if message.RoomID != model.RoomID {
			return nil
		}
		if model.Page != PageRoom || message.SenderID <= 0 ||
			(message.SenderID != model.BuilderID && message.SenderID != model.PeerBuilderID) {
			return fmt.Errorf("invalid message sender")
		}
		if err := textcheck.ValidateMessage(message.Text); err != nil {
			return err
		}
		model.appendMessage(Message{SenderID: message.SenderID, Text: message.Text})
		if message.SenderID == model.BuilderID {
			model.sendRate.Accept(time.Now())
			if model.InputError == "Message is sending" {
				model.InputError = ""
			}
			if model.messagePending && model.pendingText == message.Text &&
				model.Input.Value() == message.Text && model.inputRevision == model.pendingRevision {
				model.Input.Reset()
				model.inputRevision++
			}
			if model.messagePending && model.pendingText == message.Text {
				model.messagePending = false
				model.pendingText = ""
			}
		}
	case protocol.EventRoomEnded:
		ended, err := protocol.Payload[protocol.RoomEnded](envelope)
		if err != nil {
			return err
		}
		if ended.RoomID != model.RoomID {
			return nil
		}
		if model.Page != PageRoom {
			return fmt.Errorf("room ending received outside a room")
		}
		if ended.InitiatedBy != "self" && ended.InitiatedBy != "peer" {
			return fmt.Errorf("invalid room ending source")
		}
		if ended.Destination != protocol.DestinationHome && ended.Destination != protocol.DestinationWaiting {
			return fmt.Errorf("invalid room ending destination")
		}
		if ended.InitiatedBy == "peer" && ended.Destination != protocol.DestinationWaiting {
			return fmt.Errorf("peer room ending must continue matching")
		}
		model.clearRoom()
		if ended.InitiatedBy == "peer" {
			model.Notice = "Builder left"
		}
		if ended.Destination == protocol.DestinationHome {
			model.Page = PageHome
		} else {
			model.Page = PageWaiting
		}
	case protocol.EventError:
		problem, err := protocol.Payload[protocol.Error](envelope)
		if err != nil {
			return err
		}
		if err := problem.Validate(); err != nil {
			return err
		}
		if model.Page == PageRoom {
			model.InputError = problem.Message
		} else {
			model.Notice = problem.Message
		}
	}
	return nil
}

// SetSelfPresence updates the global bottom status independently of matching.
func (model *Model) SetSelfPresence(presence protocol.Presence) error {
	if err := presence.Validate(); err != nil {
		return err
	}
	model.SelfPresence = presence
	return nil
}

// SetHookResults updates the hook result panel without coupling the TUI to the
// configuration package.
func (model *Model) SetHookResults(results []HookResult) {
	model.HookResults = append([]HookResult(nil), results...)
	model.HooksChecked = true
}

// SetAdmissionLinks supplies release-configured public documents without
// coupling the presentation model to process configuration.
func (model *Model) SetAdmissionLinks(rulesURL, privacyURL string) {
	model.RulesURL, model.PrivacyURL = rulesURL, privacyURL
}

// ShowIdentityError opens the explicit recovery page.
func (model *Model) ShowIdentityError() { model.Page = PageIdentityError }

// FinishAdmission applies the persisted admission result. Failed writes keep
// the user on the admission page so acceptance is never merely in memory.
func (model *Model) FinishAdmission(err error) {
	if err != nil {
		model.Page = PageAdmission
		model.Notice = "Could not save acceptance."
		return
	}
	model.AdmissionDone = true
	model.Page = PageHome
	model.Notice = ""
}

// ReturnHome clears transient matching and room presentation after a terminal
// connection failure such as an expired recovery token.
func (model *Model) ReturnHome(notice string) {
	model.clearRoom()
	model.Page = PageHome
	model.Reconnecting = false
	model.Notice = notice
}

// MessageSendFailed releases the single in-flight message while preserving
// the exact draft for a deliberate retry after transport failure.
func (model *Model) MessageSendFailed() {
	if !model.messagePending {
		return
	}
	model.releasePendingMessage()
	model.InputError = "Message not sent."
}

// MessageSendRejected releases the one in-flight message after the application
// correlates a Relay error to its client event ID. ApplyRelay then supplies the
// server-authored, validated reason.
func (model *Model) MessageSendRejected() {
	model.releasePendingMessage()
}

func (model *Model) releasePendingMessage() {
	model.messagePending = false
	model.pendingText = ""
}

// ClearNotice removes a specific transient notice without clearing a newer one.
func (model *Model) ClearNotice(notice string) {
	if model.Notice == notice {
		model.Notice = ""
	}
}

func (model *Model) appendMessage(message Message) {
	model.Messages = append(model.Messages, message)
	model.messageBytes += len(message.Text)
	for len(model.Messages) > MaxRoomMessages || model.messageBytes > MaxRoomBytes {
		firstSender := model.Messages[0].SenderID
		for len(model.Messages) > 0 && model.Messages[0].SenderID == firstSender {
			model.messageBytes -= len(model.Messages[0].Text)
			model.Messages = model.Messages[1:]
		}
		model.trimmed = true
	}
	model.refreshViewport()
}

func (model *Model) clearRoom() {
	model.RoomID, model.InputError = "", ""
	model.PeerBuilderID = 0
	model.PeerPresence = protocol.Presence{State: protocol.AgentIdle}
	model.Messages = nil
	model.messageBytes, model.trimmed = 0, false
	model.messagePending = false
	model.pendingText = ""
	model.Input.Reset()
	model.inputRevision++
	model.Viewport.SetContent("")
}

func (model *Model) resize(width, height int) {
	model.Width, model.Height = width, height
	contentWidth := max(20, width-2-roomSidePadding*2)
	model.Input.SetHeight(1)
	model.Input.SetWidth(contentWidth)
	model.Viewport.Width = contentWidth
	fixedHeight := roomOuterHeight + roomHeaderHeight + inputSurfaceHeight + roomStatusHeight
	model.Viewport.Height = max(0, height-fixedHeight)
	model.refreshViewport()
}

func (model *Model) refreshViewport() {
	wasAtBottom := model.Viewport.AtBottom()
	model.Viewport.SetContent(model.messageHistoryView())
	if wasAtBottom {
		model.Viewport.GotoBottom()
	}
}

func shortValidationError(err error) string {
	switch {
	case strings.Contains(err.Error(), "empty"):
		return "Message is empty"
	case strings.Contains(err.Error(), "lines"):
		return "Too many lines"
	case strings.Contains(err.Error(), "UTF-8"):
		return "Unsupported text"
	case strings.Contains(err.Error(), "control"):
		return "Unsupported text"
	default:
		return "Message is too long"
	}
}
