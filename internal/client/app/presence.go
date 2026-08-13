package app

import (
	"errors"
	"time"

	"github.com/xfey/lobbyte/internal/client/ipc"
	"github.com/xfey/lobbyte/internal/protocol"
)

const doneDuration = 3 * time.Second

// PresenceTracker reduces allowlisted Hook events into one best-effort state.
// Generation lets callers ignore an obsolete done timer after a newer event.
type PresenceTracker struct {
	presence   protocol.Presence
	generation uint64
}

// NewPresenceTracker starts with the privacy-preserving idle state.
func NewPresenceTracker() PresenceTracker {
	return PresenceTracker{presence: protocol.Presence{State: protocol.AgentIdle}}
}

// Presence returns the current reduced state.
func (tracker PresenceTracker) Presence() protocol.Presence { return tracker.presence }

// Apply maps the most recently received valid Hook event to a presence. A
// non-zero deadline is returned only for done, together with its generation.
func (tracker *PresenceTracker) Apply(event ipc.Event, now time.Time) (protocol.Presence, uint64, time.Time, error) {
	if event.Source != protocol.SourceCodex && event.Source != protocol.SourceClaudeCode {
		return tracker.presence, tracker.generation, time.Time{}, errors.New("unsupported agent source")
	}
	var state protocol.AgentState
	switch event.Name {
	case "SessionStart":
		state = protocol.AgentPrompting
	case "UserPromptSubmit":
		state = protocol.AgentWaiting
	case "Stop":
		state = protocol.AgentDone
	case "SessionEnd":
		state = protocol.AgentIdle
	default:
		return tracker.presence, tracker.generation, time.Time{}, errors.New("unsupported hook event")
	}
	tracker.generation++
	tracker.presence = protocol.Presence{State: state, Source: event.Source}
	if state == protocol.AgentIdle {
		tracker.presence.Source = protocol.SourceNone
	}
	if state == protocol.AgentDone {
		return tracker.presence, tracker.generation, now.Add(doneDuration), nil
	}
	return tracker.presence, tracker.generation, time.Time{}, nil
}

// ExpireDone changes the matching done generation to prompting. Older timers
// and timers arriving after any newer event have no effect.
func (tracker *PresenceTracker) ExpireDone(generation uint64) (protocol.Presence, bool) {
	if generation != tracker.generation || tracker.presence.State != protocol.AgentDone {
		return tracker.presence, false
	}
	tracker.generation++
	tracker.presence.State = protocol.AgentPrompting
	return tracker.presence, true
}
