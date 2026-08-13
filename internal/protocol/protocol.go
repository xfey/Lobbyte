// Package protocol contains the versioned client and Relay wire types.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// Version is the only protocol version supported by this release.
	Version = 1
	// MaxFrameBytes is enforced before a complete JSON event is decoded.
	MaxFrameBytes = 16 * 1024
)

var ErrUnsupportedVersion = errors.New("unsupported protocol version")

// EventType identifies an application event carried by an Envelope.
type EventType string

const (
	EventHello           EventType = "hello"
	EventReady           EventType = "ready"
	EventMatchJoin       EventType = "match.join"
	EventMatchCancel     EventType = "match.cancel"
	EventMatchWaiting    EventType = "match.waiting"
	EventPresenceSet     EventType = "presence.set"
	EventPresenceChanged EventType = "presence.changed"
	EventMessageSend     EventType = "message.send"
	EventMessageReceived EventType = "message.received"
	EventRoomNext        EventType = "room.next"
	EventRoomExit        EventType = "room.exit"
	EventRoomBlock       EventType = "room.block"
	EventRoomJoined      EventType = "room.joined"
	EventRoomEnded       EventType = "room.ended"
	EventError           EventType = "error"
)

// AgentState is the minimal user-visible state derived from local hooks.
type AgentState string

const (
	AgentIdle      AgentState = "idle"
	AgentPrompting AgentState = "prompting"
	AgentWaiting   AgentState = "waiting"
	AgentDone      AgentState = "done"
)

// AgentSource identifies the most recently active supported coding agent.
type AgentSource string

const (
	SourceNone       AgentSource = ""
	SourceCodex      AgentSource = "codex"
	SourceClaudeCode AgentSource = "claude-code"
)

// Presence is the only hook-derived information sent to the Relay.
type Presence struct {
	State  AgentState  `json:"state"`
	Source AgentSource `json:"source,omitempty"`
}

// Validate ensures idle never exposes a source and active states always do.
func (presence Presence) Validate() error {
	switch presence.State {
	case AgentIdle:
		if presence.Source != SourceNone {
			return errors.New("idle presence must not include an agent source")
		}
	case AgentPrompting, AgentWaiting, AgentDone:
		if presence.Source != SourceCodex && presence.Source != SourceClaudeCode {
			return errors.New("active presence requires a supported agent source")
		}
	default:
		return fmt.Errorf("unsupported agent state %q", presence.State)
	}
	return nil
}

// Envelope is the stable outer shape for every client and Relay event.
type Envelope struct {
	Version int             `json:"v"`
	Type    EventType       `json:"type"`
	ID      string          `json:"id"`
	Data    json.RawMessage `json:"data"`
}

// New creates an event envelope from a typed payload.
func New(eventType EventType, id string, payload any) (Envelope, error) {
	if err := validateEventID(id); err != nil {
		return Envelope{}, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode %s payload: %w", eventType, err)
	}
	return Envelope{Version: Version, Type: eventType, ID: id, Data: data}, nil
}

// Decode validates the stable envelope while allowing future payload fields.
func Decode(data []byte) (Envelope, error) {
	if len(data) > MaxFrameBytes {
		return Envelope{}, fmt.Errorf("event exceeds %d bytes", MaxFrameBytes)
	}
	if !utf8.Valid(data) {
		return Envelope{}, errors.New("event is not valid UTF-8")
	}
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode event: %w", err)
	}
	if envelope.Version != Version {
		return Envelope{}, fmt.Errorf("%w %d", ErrUnsupportedVersion, envelope.Version)
	}
	if envelope.Type == "" {
		return Envelope{}, errors.New("event type is required")
	}
	if err := validateEventID(envelope.ID); err != nil {
		return Envelope{}, err
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return Envelope{}, errors.New("event data is required")
	}
	return envelope, nil
}

// Payload decodes an envelope payload into its expected type.
func Payload[T any](envelope Envelope) (T, error) {
	var payload T
	if err := json.Unmarshal(envelope.Data, &payload); err != nil {
		return payload, fmt.Errorf("decode %s payload: %w", envelope.Type, err)
	}
	return payload, nil
}

// NewEventID returns a non-identifying random event identifier.
func NewEventID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate event id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func validateEventID(id string) error {
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return errors.New("event id must contain 1 to 128 bytes")
	}
	return nil
}

// Hello begins every WebSocket session.
type Hello struct {
	DeviceID      string `json:"device_id"`
	ClientVersion string `json:"client_version"`
	RulesVersion  string `json:"rules_version"`
	ResumeToken   string `json:"resume_token,omitempty"`
}

// Ready confirms identity registration and connection recovery.
type Ready struct {
	BuilderID   int64  `json:"builder_id"`
	ResumeToken string `json:"resume_token"`
	Recovered   bool   `json:"recovered"`
}

// MatchJoin identifies one user-initiated matching intent across transport
// reconnects. A new intent is created only after returning home.
type MatchJoin struct {
	IntentID string `json:"intent_id"`
}

// RoomEvent identifies the room affected by a client command.
type RoomEvent struct {
	RoomID string `json:"room_id"`
}

// RoomJoined describes a newly matched or recovered room.
type RoomJoined struct {
	RoomID    string   `json:"room_id"`
	BuilderID int64    `json:"builder_id"`
	Presence  Presence `json:"presence"`
	Recovered bool     `json:"recovered"`
}

// PresenceChanged identifies the room whose peer presence changed so delayed
// events cannot affect a later room.
type PresenceChanged struct {
	RoomID   string   `json:"room_id"`
	Presence Presence `json:"presence"`
}

// MessageSend contains the current room and the user's plain text.
type MessageSend struct {
	RoomID string `json:"room_id"`
	Text   string `json:"text"`
}

// MessageReceived is atomically queued to both room members.
type MessageReceived struct {
	RoomID   string `json:"room_id"`
	SenderID int64  `json:"sender_id"`
	Text     string `json:"text"`
}

// Destination tells a client where to go after a room ends.
type Destination string

const (
	DestinationHome    Destination = "home"
	DestinationWaiting Destination = "waiting"
)

// RoomEnded contains only the information required by the receiving client.
type RoomEnded struct {
	RoomID      string      `json:"room_id"`
	InitiatedBy string      `json:"initiated_by"`
	Destination Destination `json:"destination"`
}

// Error is a stable, user-safe protocol error.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	EventID   string `json:"event_id,omitempty"`
	Retryable bool   `json:"retryable"`
}

// Validate ensures a Relay error can be displayed as one bounded, inert line.
// Error text remains server-authored for forward compatibility, but it cannot
// carry terminal controls or an unbounded payload into the client UI.
func (problem Error) Validate() error {
	if problem.Code == "" || len(problem.Code) > 64 {
		return errors.New("error code must contain 1 to 64 bytes")
	}
	for _, value := range problem.Code {
		if !(value == '_' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9') {
			return errors.New("error code contains unsupported characters")
		}
	}
	if strings.TrimSpace(problem.Message) == "" || len(problem.Message) > 256 || !utf8.ValidString(problem.Message) {
		return errors.New("error message must contain 1 to 256 UTF-8 bytes")
	}
	for _, value := range problem.Message {
		if value == '\n' || value <= 0x1f || value >= 0x7f && value <= 0x9f || isBidiControl(value) {
			return errors.New("error message contains a display control character")
		}
	}
	if problem.EventID != "" {
		if err := validateEventID(problem.EventID); err != nil {
			return fmt.Errorf("invalid referenced event id: %w", err)
		}
	}
	return nil
}

func isBidiControl(value rune) bool {
	switch value {
	case 0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2066, 0x2067, 0x2068, 0x2069:
		return true
	default:
		return false
	}
}
