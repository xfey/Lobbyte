package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeIdentifiesUnsupportedVersion(t *testing.T) {
	t.Parallel()

	_, err := Decode([]byte(`{"v":2,"type":"hello","id":"event-1","data":{}}`))
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Decode error = %v", err)
	}
}

func TestPresenceValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		presence Presence
		valid    bool
	}{
		{name: "idle", presence: Presence{State: AgentIdle}, valid: true},
		{name: "idle with source", presence: Presence{State: AgentIdle, Source: SourceCodex}},
		{name: "waiting for codex", presence: Presence{State: AgentWaiting, Source: SourceCodex}, valid: true},
		{name: "active without source", presence: Presence{State: AgentDone}},
		{name: "unknown", presence: Presence{State: "busy", Source: SourceCodex}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.presence.Validate()
			if (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestEnvelopeRoundTripAllowsFuturePayloadFields(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"v":1,"type":"presence.set","id":"event-1","data":{"state":"idle","future":true}}`)
	envelope, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned an error: %v", err)
	}
	presence, err := Payload[Presence](envelope)
	if err != nil {
		t.Fatalf("Payload returned an error: %v", err)
	}
	if presence.State != AgentIdle {
		t.Fatalf("unexpected state: %q", presence.State)
	}
}

func TestDecodeRejectsInvalidEnvelope(t *testing.T) {
	t.Parallel()

	tests := [][]byte{
		[]byte(`{"v":2,"type":"hello","id":"event-1","data":{}}`),
		[]byte(`{"v":1,"type":"","id":"event-1","data":{}}`),
		[]byte(`{"v":1,"type":"hello","id":"","data":{}}`),
		[]byte(`{"v":1,"type":"hello","id":"event-1"}`),
		[]byte(`{"v":1,"type":"hello","id":"event-1","data":null}`),
		append([]byte(`{"v":1,"type":"hello","id":"event-1","data":{"value":"`), 0xff, '"', '}', '}'),
		[]byte(strings.Repeat("x", MaxFrameBytes+1)),
	}
	for _, raw := range tests {
		if _, err := Decode(raw); err == nil {
			t.Fatalf("Decode accepted invalid input %q", string(raw[:min(len(raw), 80)]))
		}
	}
}

func TestNewProducesJSONPayload(t *testing.T) {
	t.Parallel()

	envelope, err := New(EventMatchJoin, "event-1", struct{}{})
	if err != nil {
		t.Fatalf("New returned an error: %v", err)
	}
	if !json.Valid(envelope.Data) {
		t.Fatalf("payload is not valid JSON: %q", envelope.Data)
	}
}

func TestErrorValidationAllowsSafeBoundedTextOnly(t *testing.T) {
	t.Parallel()

	valid := Error{Code: "storage_unavailable", Message: "Relay is temporarily unavailable.", EventID: "event-1", Retryable: true}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid error rejected: %v", err)
	}
	for _, problem := range []Error{
		{Code: "", Message: "missing code"},
		{Code: "Bad-Code", Message: "bad code"},
		{Code: "operation_failed", Message: "unsafe\nline"},
		{Code: "operation_failed", Message: "unsafe\x1b[31m"},
		{Code: "operation_failed", Message: strings.Repeat("x", 257)},
		{Code: "operation_failed", Message: "safe", EventID: strings.Repeat("x", 129)},
	} {
		if err := problem.Validate(); err == nil {
			t.Fatalf("invalid error accepted: %#v", problem)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"v":1,"type":"hello","id":"event-1","data":{}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = Decode(raw)
	})
}
