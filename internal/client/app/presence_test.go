package app

import (
	"testing"
	"time"

	"github.com/xfey/lobbyte/internal/client/ipc"
	"github.com/xfey/lobbyte/internal/protocol"
)

func TestPresenceTrackerUsesLatestSourceAndIgnoresOldDoneTimer(t *testing.T) {
	t.Parallel()

	tracker := NewPresenceTracker()
	now := time.Unix(100, 0)
	done, generation, deadline, err := tracker.Apply(ipc.Event{Source: protocol.SourceCodex, Name: "Stop"}, now)
	if err != nil || done.State != protocol.AgentDone || !deadline.Equal(now.Add(3*time.Second)) {
		t.Fatalf("done result = %#v, %d, %v, %v", done, generation, deadline, err)
	}
	waiting, _, _, err := tracker.Apply(ipc.Event{Source: protocol.SourceClaudeCode, Name: "UserPromptSubmit"}, now)
	if err != nil || waiting.State != protocol.AgentWaiting || waiting.Source != protocol.SourceClaudeCode {
		t.Fatalf("latest state = %#v, err = %v", waiting, err)
	}
	if presence, changed := tracker.ExpireDone(generation); changed || presence != waiting {
		t.Fatalf("old timer changed state: %#v, %v", presence, changed)
	}
}

func TestPresenceTrackerIdleHidesSourceAndCurrentDoneExpires(t *testing.T) {
	t.Parallel()

	tracker := NewPresenceTracker()
	_, generation, _, _ := tracker.Apply(ipc.Event{Source: protocol.SourceCodex, Name: "Stop"}, time.Now())
	prompting, changed := tracker.ExpireDone(generation)
	if !changed || prompting.State != protocol.AgentPrompting || prompting.Source != protocol.SourceCodex {
		t.Fatalf("done expiry = %#v, %v", prompting, changed)
	}
	idle, _, _, err := tracker.Apply(ipc.Event{Source: protocol.SourceClaudeCode, Name: "SessionEnd"}, time.Now())
	if err != nil || idle.State != protocol.AgentIdle || idle.Source != protocol.SourceNone {
		t.Fatalf("idle = %#v, err = %v", idle, err)
	}
}
