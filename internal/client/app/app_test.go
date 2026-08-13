package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
	"github.com/xfey/lobbyte/internal/client/ipc"
	"github.com/xfey/lobbyte/internal/client/network"
	"github.com/xfey/lobbyte/internal/client/tui"
	"github.com/xfey/lobbyte/internal/protocol"
)

func TestRunStopsAfterContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	root := t.TempDir()
	var output bytes.Buffer
	if err := Run(ctx, &output, Config{
		ProfileDir: root, RuntimeDir: filepath.Join(root, "runtime"),
		RelayURL: "ws://127.0.0.1:1/v1/ws", ClientVersion: "test", RulesVersion: "test-1",
		HomeDir: root, Executable: filepath.Join(root, "lobbyte"), Input: io.NopCloser(bytes.NewReader(nil)),
	}); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
}

func TestUnconfirmedBlockReturnsHome(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{
		view: tui.NewModel(true), intent: true,
		pendingRoomCommand: protocol.EventRoomBlock,
	}
	model.view.Page = tui.PageWaiting
	updated, _ := model.Update(sendResultMsg{eventType: protocol.EventRoomBlock, err: errors.New("network failed")})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageHome || result.view.Notice != "Could not confirm block." || result.intent {
		t.Fatalf("block failure state: page=%d notice=%q intent=%v", result.view.Page, result.view.Notice, result.intent)
	}
}

func TestNextRetriesWhileOldSessionIsStillRecoverable(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{
		view:               tui.NewModel(true),
		intent:             true,
		intentID:           "intent",
		generation:         3,
		transportRetry:     true,
		pendingRoomCommand: protocol.EventRoomNext,
	}
	model.view.Page = tui.PageWaiting
	updated, command := model.Update(connectResultMsg{
		generation: 3,
		err: &network.DialError{
			Code: "already_connected", Message: "Lobbyte is already connected.", Retryable: false,
		},
	})
	result := updated.(*runtimeModel)
	if !result.intent || !result.view.Reconnecting || !result.transportRetry ||
		result.pendingRoomCommand != protocol.EventRoomNext || command == nil {
		t.Fatalf("next retry state: intent=%v reconnecting=%v pending=%s command=%v",
			result.intent, result.view.Reconnecting, result.pendingRoomCommand, command)
	}
}

func TestNextDuringReconnectInvalidatesAStaleRoomRecovery(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{
		view: tui.NewModel(true), intent: true, intentID: "stable-intent",
		resume: "old-room-token", generation: 4, transportRetry: true,
	}
	model.view.Page = tui.PageWaiting
	commands := model.applyEffect(tui.Command{
		Type: protocol.EventRoomNext, Payload: protocol.RoomEvent{RoomID: "old-room"},
	})
	if model.generation != 5 || model.resume != "" || !model.view.Reconnecting || len(commands) != 2 {
		t.Fatalf("next reconnect state: generation=%d resume=%q reconnecting=%v commands=%d",
			model.generation, model.resume, model.view.Reconnecting, len(commands))
	}

	staleSession := testNetworkSession(t)
	updated, _ := model.Update(connectResultMsg{generation: 4, deviceID: "device", session: staleSession})
	result := updated.(*runtimeModel)
	if result.session != nil || result.view.Page != tui.PageWaiting || result.generation != 5 ||
		result.pendingRoomCommand != protocol.EventRoomNext {
		t.Fatalf("stale recovery changed next state: session=%p page=%d generation=%d pending=%s",
			result.session, result.view.Page, result.generation, result.pendingRoomCommand)
	}
}

func TestInitialAlreadyConnectedDoesNotRetryForever(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{view: tui.NewModel(true), intent: true, intentID: "intent", generation: 1}
	model.view.Page = tui.PageWaiting
	updated, _ := model.Update(connectResultMsg{
		generation: 1,
		err: &network.DialError{
			Code: "already_connected", Message: "Lobbyte is already connected.", Retryable: false,
		},
	})
	result := updated.(*runtimeModel)
	if result.intent || result.view.Reconnecting || result.view.Page != tui.PageHome {
		t.Fatalf("initial duplicate connection state: intent=%v reconnecting=%v page=%d",
			result.intent, result.view.Reconnecting, result.view.Page)
	}
}

func TestRoomTransportRaceRetriesWithRecoveryToken(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{
		view:           tui.NewModel(true),
		intent:         true,
		intentID:       "intent",
		resume:         "room-recovery-token",
		generation:     4,
		transportRetry: true,
	}
	model.view.Page = tui.PageRoom
	updated, command := model.Update(connectResultMsg{
		generation: 4,
		err: &network.DialError{
			Code: "already_connected", Message: "Lobbyte is already connected.", Retryable: false,
		},
	})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageRoom || !result.intent || !result.view.Reconnecting ||
		!result.transportRetry || result.resume != "room-recovery-token" || command == nil {
		t.Fatalf("room retry state: page=%d intent=%v reconnecting=%v transport_retry=%v resume=%q command=%v",
			result.view.Page, result.intent, result.view.Reconnecting, result.transportRetry, result.resume, command)
	}
}

func TestWaitingTransportLossRetriesWithoutRoomToken(t *testing.T) {
	t.Parallel()

	session := testNetworkSession(t)
	model := &runtimeModel{
		view: tui.NewModel(true), intent: true, intentID: "stable-intent", resume: "unused-token",
		generation: 2, session: session,
	}
	model.view.Page = tui.PageWaiting
	updated, command := model.Update(relayClosedMsg{session: session, err: errors.New("network lost")})
	result := updated.(*runtimeModel)
	if result.session != nil || !result.intent || !result.transportRetry || !result.view.Reconnecting ||
		result.resume != "" || result.intentID != "stable-intent" || command == nil {
		t.Fatalf("waiting reconnect state: session=%p intent=%v transport_retry=%v reconnecting=%v resume=%q intent_id=%q command=%v",
			result.session, result.intent, result.transportRetry, result.view.Reconnecting,
			result.resume, result.intentID, command)
	}
}

func TestRelayRestartDuringRoomReturnsHomeAfterRecoveryFails(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{
		view: tui.NewModel(true), intent: true, intentID: "intent", resume: "lost-with-relay",
		generation: 6, transportRetry: true,
	}
	model.view.Page, model.view.RoomID = tui.PageRoom, "old-room"
	updated, _ := model.Update(connectResultMsg{
		generation: 6,
		err: &network.DialError{
			Code: "resume_failed", Message: "Could not restore room.", Retryable: false,
		},
	})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageHome || result.view.RoomID != "" || result.intent ||
		result.view.Reconnecting || result.resume != "" || result.view.Notice != "Could not restore room." {
		t.Fatalf("Relay restart recovery state: page=%d room=%q intent=%v reconnecting=%v resume=%q notice=%q",
			result.view.Page, result.view.RoomID, result.intent, result.view.Reconnecting,
			result.resume, result.view.Notice)
	}
}

func TestSessionProtocolIncompatibilityStopsReconnect(t *testing.T) {
	t.Parallel()

	session := testNetworkSession(t)
	model := &runtimeModel{
		view: tui.NewModel(true), intent: true, intentID: "intent", resume: "room-token",
		generation: 2, session: session,
	}
	model.view.Page, model.view.RoomID, model.view.Reconnecting = tui.PageRoom, "room", true
	updated, _ := model.Update(relayClosedMsg{session: session, err: &network.DialError{
		Code: "protocol_incompatible", Message: "Update Lobbyte to continue.", Retryable: false,
	}})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageHome || result.view.RoomID != "" || result.intent ||
		result.transportRetry || result.view.Reconnecting || result.resume != "" ||
		result.view.Notice != "Update Lobbyte to continue." {
		t.Fatalf("protocol incompatibility state: page=%d room=%q intent=%v transport_retry=%v reconnecting=%v resume=%q notice=%q",
			result.view.Page, result.view.RoomID, result.intent, result.transportRetry,
			result.view.Reconnecting, result.resume, result.view.Notice)
	}
}

func TestWaitingResumeFailureWithoutNextReturnsHome(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{
		view: tui.NewModel(true), intent: true, intentID: "intent", resume: "stale-token",
		generation: 5, transportRetry: true,
	}
	model.view.Page = tui.PageWaiting
	updated, _ := model.Update(connectResultMsg{
		generation: 5,
		err: &network.DialError{
			Code: "resume_failed", Message: "Could not restore room.", Retryable: false,
		},
	})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageHome || result.intent || result.view.Reconnecting || result.resume != "" {
		t.Fatalf("stale waiting recovery state: page=%d intent=%v reconnecting=%v resume=%q",
			result.view.Page, result.intent, result.view.Reconnecting, result.resume)
	}
}

func TestRetryableRoomOperationErrorDoesNotDisconnect(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{view: tui.NewModel(true), intent: true, resume: "room-token"}
	model.view.Page, model.view.RoomID = tui.PageRoom, "room"
	session := &network.Session{}
	model.session = session
	event, err := protocol.New(protocol.EventError, "error", protocol.Error{
		Code: "capacity", Message: "Relay is temporarily unavailable.", Retryable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(relayEventMsg{session: session, event: event})
	result := updated.(*runtimeModel)
	if result.session != session || result.view.Page != tui.PageRoom || result.view.Reconnecting ||
		result.resume != "room-token" || result.view.InputError != "Relay is temporarily unavailable." || command == nil {
		t.Fatalf("room operation error state: session=%p page=%d reconnecting=%v resume=%q input_error=%q command=%v",
			result.session, result.view.Page, result.view.Reconnecting, result.resume, result.view.InputError, command)
	}
}

func TestInvalidRelayRoomEventReturnsHomeWithoutRetrying(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{view: tui.NewModel(true), intent: true, resume: "room-token"}
	model.view.Page, model.view.RoomID, model.view.BuilderID = tui.PageRoom, "room", 1
	model.view.PeerBuilderID = 2
	event, err := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{
		RoomID: "room", SenderID: 999, Text: "unknown member",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(relayEventMsg{event: event})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageHome || result.view.Notice != "Relay sent an invalid event." ||
		result.intent || result.resume != "" || result.view.Reconnecting || command != nil {
		t.Fatalf("invalid Relay event state: page=%d notice=%q intent=%v resume=%q reconnecting=%v command=%v",
			result.view.Page, result.view.Notice, result.intent, result.resume, result.view.Reconnecting, command)
	}
}

func TestOnlyCorrelatedRelayErrorReleasesPendingMessage(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{
		view: tui.NewModel(true), intent: true,
		pendingMessageID: "message-event", pendingMessageText: "hello",
	}
	model.view.Page, model.view.RoomID, model.view.BuilderID = tui.PageRoom, "room", 1
	model.view.Width, model.view.Height = 80, 24
	model.view.Input.SetValue("hello")
	updatedView, _ := model.view.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model.view = updatedView.(tui.Model)
	_ = model.view.DrainEffects()

	unrelated, _ := protocol.New(protocol.EventError, "unrelated", protocol.Error{
		Code: "operation_failed", Message: "Could not complete operation.", EventID: "presence-event",
	})
	updated, _ := model.Update(relayEventMsg{event: unrelated})
	model = updated.(*runtimeModel)
	updatedView, _ = model.view.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model.view = updatedView.(tui.Model)
	if model.pendingMessageID != "message-event" || model.view.InputError != "Message is sending" {
		t.Fatalf("unrelated error released pending message: id=%q error=%q",
			model.pendingMessageID, model.view.InputError)
	}

	correlated, _ := protocol.New(protocol.EventError, "correlated", protocol.Error{
		Code: "operation_failed", Message: "Could not complete operation.", EventID: "message-event",
	})
	updated, _ = model.Update(relayEventMsg{event: correlated})
	model = updated.(*runtimeModel)
	if model.pendingMessageID != "" || model.view.Input.Value() != "hello" ||
		model.view.InputError != "Could not complete operation." {
		t.Fatalf("correlated error state: id=%q input=%q error=%q",
			model.pendingMessageID, model.view.Input.Value(), model.view.InputError)
	}
}

func TestMismatchedSelfEchoReturnsHome(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{
		view: tui.NewModel(true), intent: true, resume: "room-token",
		pendingMessageID: "message-event", pendingMessageText: "expected",
	}
	model.view.Page, model.view.RoomID, model.view.BuilderID = tui.PageRoom, "room", 1
	model.view.PeerBuilderID = 2
	event, _ := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{
		RoomID: "room", SenderID: 1, Text: "different",
	})
	updated, _ := model.Update(relayEventMsg{event: event})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageHome || result.intent || result.resume != "" ||
		result.view.Notice != "Relay sent an invalid event." || len(result.view.Messages) != 0 {
		t.Fatalf("mismatched echo state: page=%d intent=%v resume=%q notice=%q messages=%#v",
			result.view.Page, result.intent, result.resume, result.view.Notice, result.view.Messages)
	}
}

func TestUnsolicitedSelfMessageReturnsHome(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{view: tui.NewModel(true), intent: true, resume: "room-token"}
	model.view.Page, model.view.RoomID, model.view.BuilderID = tui.PageRoom, "room", 1
	model.view.PeerBuilderID = 2
	event, _ := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{
		RoomID: "room", SenderID: 1, Text: "unsolicited echo",
	})
	updated, _ := model.Update(relayEventMsg{event: event})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageHome || result.intent || result.resume != "" ||
		result.view.Notice != "Relay sent an invalid event." || len(result.view.Messages) != 0 {
		t.Fatalf("unsolicited echo state: page=%d intent=%v resume=%q notice=%q messages=%#v",
			result.view.Page, result.intent, result.resume, result.view.Notice, result.view.Messages)
	}
}

func TestLateSelfEchoFromPreviousRoomIsIgnored(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{view: tui.NewModel(true), intent: true, resume: "current-token"}
	model.view.Page, model.view.RoomID, model.view.BuilderID = tui.PageRoom, "current-room", 1
	model.view.PeerBuilderID = 2
	event, _ := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{
		RoomID: "previous-room", SenderID: 1, Text: "late echo",
	})
	updated, _ := model.Update(relayEventMsg{event: event})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageRoom || result.view.RoomID != "current-room" || !result.intent ||
		result.resume != "current-token" || len(result.view.Messages) != 0 {
		t.Fatalf("late echo changed current room: page=%d room=%q intent=%v resume=%q messages=%#v",
			result.view.Page, result.view.RoomID, result.intent, result.resume, result.view.Messages)
	}
}

func TestMessageTransportFailureKeepsDraftAndShowsReason(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{view: tui.NewModel(true), intent: true}
	model.view.Page, model.view.RoomID = tui.PageRoom, "room"
	model.view.Width, model.view.Height = 80, 24
	model.view.Input.SetValue("unsent draft")
	updatedView, _ := model.view.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model.view = updatedView.(tui.Model)
	_ = model.view.DrainEffects()
	session := &network.Session{}
	model.session, model.pendingMessageID = session, "message-event"
	updated, command := model.Update(sendResultMsg{
		session: session, eventType: protocol.EventMessageSend, eventID: "message-event", err: errors.New("write failed"),
	})
	result := updated.(*runtimeModel)
	if result.view.Input.Value() != "unsent draft" || result.view.InputError != "Message not sent." || command == nil {
		t.Fatalf("message failure state: draft=%q error=%q command=%v",
			result.view.Input.Value(), result.view.InputError, command)
	}
}

func TestMessageDuringReconnectKeepsDraftAndShowsReason(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{view: tui.NewModel(true), intent: true}
	model.view.Page, model.view.RoomID = tui.PageRoom, "room"
	model.view.Width, model.view.Height = 80, 24
	model.view.Reconnecting = true
	model.view.Input.SetValue("keep this draft")
	updatedView, _ := model.view.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model.view = updatedView.(tui.Model)
	effects := model.view.DrainEffects()
	if len(effects) != 1 {
		t.Fatalf("message effects = %#v", effects)
	}

	commands := model.applyEffect(effects[0])
	if len(commands) != 0 || model.view.Input.Value() != "keep this draft" || model.view.InputError != "Message not sent." {
		t.Fatalf("reconnecting send: commands=%d draft=%q error=%q",
			len(commands), model.view.Input.Value(), model.view.InputError)
	}
}

func TestBlockWithoutTransportReturnsHomeAsUnconfirmed(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{view: tui.NewModel(true), intent: true, resume: "old-room-token"}
	model.view.Page, model.view.RoomID = tui.PageRoom, "room"
	model.view.Width, model.view.Height = 80, 24
	model.view.Input.SetValue("/block")
	model.view.CommandOpen = true
	result := model
	updated, command := result.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(*runtimeModel)
	if result.view.Page != tui.PageHome || result.view.Notice != "Could not confirm block." ||
		result.intent || result.resume != "" || command == nil {
		t.Fatalf("block without transport: page=%d notice=%q intent=%v resume=%q command=%v",
			result.view.Page, result.view.Notice, result.intent, result.resume, command)
	}
}

func TestRoomTransitionsRestartWaitingAnimation(t *testing.T) {
	t.Parallel()

	for _, eventType := range []protocol.EventType{protocol.EventRoomNext, protocol.EventRoomBlock} {
		model := &runtimeModel{view: tui.NewModel(true), intent: true, session: testNetworkSession(t)}
		model.view.Page, model.view.RoomID = tui.PageRoom, "room"
		commands := model.applyEffect(tui.Command{
			Type: eventType, Payload: protocol.RoomEvent{RoomID: "room"},
		})
		if len(commands) != 2 {
			t.Fatalf("%s commands = %d, want spinner and send", eventType, len(commands))
		}
	}

	model := &runtimeModel{view: tui.NewModel(true), intent: true}
	model.view.Page, model.view.RoomID = tui.PageRoom, "room"
	session := testNetworkSession(t)
	model.session = session
	event, _ := protocol.New(protocol.EventRoomEnded, "ended", protocol.RoomEnded{
		RoomID: "room", InitiatedBy: "peer", Destination: protocol.DestinationWaiting,
	})
	updated, command := model.Update(relayEventMsg{session: session, event: event})
	result := updated.(*runtimeModel)
	if result.view.Page != tui.PageWaiting || command == nil {
		t.Fatalf("peer leave transition: page=%d command=%v", result.view.Page, command)
	}
}

func TestPresenceUpdatesAreSerializedAndLatestStateIsResent(t *testing.T) {
	t.Parallel()

	session := testNetworkSession(t)
	model := &runtimeModel{
		view: tui.NewModel(true), intent: true, session: session,
		tracker: NewPresenceTracker(),
	}
	first, generation, _, err := model.tracker.Apply(ipc.Event{
		Source: protocol.SourceCodex, Name: "UserPromptSubmit",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if command := model.queuePresence(); command == nil || !model.presenceSending || model.presenceDirty {
		t.Fatalf("first presence was not queued: sending=%v dirty=%v command=%v",
			model.presenceSending, model.presenceDirty, command)
	}
	if _, _, _, err := model.tracker.Apply(ipc.Event{
		Source: protocol.SourceClaudeCode, Name: "Stop",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if command := model.queuePresence(); command != nil || !model.presenceDirty {
		t.Fatalf("concurrent presence was not coalesced: dirty=%v command=%v", model.presenceDirty, command)
	}
	if _, changed := model.tracker.ExpireDone(generation); changed {
		t.Fatal("obsolete done generation unexpectedly changed current presence")
	}

	updated, command := model.Update(sendResultMsg{session: session, eventType: protocol.EventPresenceSet})
	result := updated.(*runtimeModel)
	if command == nil || !result.presenceSending || result.presenceDirty || result.tracker.Presence() !=
		(protocol.Presence{State: protocol.AgentDone, Source: protocol.SourceClaudeCode}) {
		t.Fatalf("latest presence was not resent: sending=%v dirty=%v presence=%#v command=%v first=%#v",
			result.presenceSending, result.presenceDirty, result.tracker.Presence(), command, first)
	}
}

func TestStopConnectionClearsPendingRoomCommand(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{intent: true, intentID: "intent", pendingRoomCommand: protocol.EventRoomNext}
	model.stopConnection()
	if model.intent || model.intentID != "" || model.pendingRoomCommand != "" {
		t.Fatalf("connection state was not cleared: %#v", model)
	}
}

func TestRetryBackoffIsBoundedAndJitteredWithinQuarter(t *testing.T) {
	t.Parallel()

	model := &runtimeModel{generation: 7}
	for index := 0; index < 16; index++ {
		delay := time.Duration(1<<min(index, 3)) * 250 * time.Millisecond
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
		jitter := retryJitter(delay / 4)
		if jitter < 0 || jitter >= delay/4 {
			t.Fatalf("retry jitter %v is outside [0, %v)", jitter, delay/4)
		}
		_ = model.retryCommand()
	}
	if model.retry != 16 {
		t.Fatalf("retry count = %d", model.retry)
	}
}

func TestConfigRequiresAbsoluteProfileDirectory(t *testing.T) {
	t.Parallel()

	err := (Config{ProfileDir: "relative"}).Validate()
	if err == nil {
		t.Fatal("Validate accepted a relative profile directory")
	}
}

func TestConfigValidatesPublishedAdmissionURLs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := Config{
		ProfileDir: root, RuntimeDir: filepath.Join(root, "runtime"), HomeDir: root,
		Executable: filepath.Join(root, "lobbyte"), RelayURL: "wss://relay.example/v1/ws",
		ClientVersion: "test", RulesVersion: "rules-1",
	}
	for _, test := range []struct {
		name       string
		rulesURL   string
		privacyURL string
		valid      bool
	}{
		{name: "development fixture", valid: true},
		{name: "published", rulesURL: "https://lobbyte.online/rules", privacyURL: "https://lobbyte.online/privacy", valid: true},
		{name: "missing pair", rulesURL: "https://lobbyte.online/rules"},
		{name: "plaintext", rulesURL: "http://lobbyte.online/rules", privacyURL: "https://lobbyte.online/privacy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := base
			config.RulesURL, config.PrivacyURL = test.rulesURL, test.privacyURL
			if err := config.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestRunRejectsPlaintextRelayOutsideIsolatedLocalProfile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := Config{
		ProfileDir: root, RuntimeDir: filepath.Join(t.TempDir(), "runtime"),
		RelayURL: "ws://relay.example/v1/ws", ClientVersion: "test", RulesVersion: "rules",
		HomeDir: root, Executable: filepath.Join(root, "lobbyte"), Input: strings.NewReader(""),
	}
	err := Run(context.Background(), io.Discard, config)
	if err == nil || !strings.Contains(err.Error(), "plaintext Relay") {
		t.Fatalf("Run error = %v", err)
	}
}

func testNetworkSession(t *testing.T) *network.Session {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if _, _, err := connection.Read(ctx); err != nil {
			return
		}
		ready, _ := protocol.New(protocol.EventReady, "ready", protocol.Ready{
			BuilderID: 1, ResumeToken: "test-resume-token",
		})
		data, _ := json.Marshal(ready)
		if err := connection.Write(ctx, websocket.MessageText, data); err != nil {
			return
		}
		<-request.Context().Done()
	}))
	t.Cleanup(httpServer.Close)
	session, err := network.Dial(context.Background(), network.Config{
		URL:      "ws" + strings.TrimPrefix(httpServer.URL, "http"),
		DeviceID: "00000000-0000-4000-8000-000000000001", ClientVersion: "test", RulesVersion: "test-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
