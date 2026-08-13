// Package network owns the client WebSocket lifecycle and protocol adapter.
package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/xfey/lobbyte/internal/protocol"
)

const operationTimeout = 5 * time.Second

var errInvalidRelayEvent = errors.New("invalid Relay event")

type invalidRelayError struct{ cause error }

func (problem *invalidRelayError) Error() string {
	return "invalid Relay event: " + problem.cause.Error()
}
func (problem *invalidRelayError) Unwrap() []error {
	return []error{errInvalidRelayEvent, problem.cause}
}

// DialError preserves a Relay rejection code and whether retrying is safe.
type DialError struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (problem *DialError) Error() string {
	if problem.Message != "" {
		return problem.Message
	}
	return problem.Cause.Error()
}

func (problem *DialError) Unwrap() error { return problem.Cause }

// Config contains the fixed session handshake values.
type Config struct {
	URL           string
	DeviceID      string
	ClientVersion string
	RulesVersion  string
	ResumeToken   string
}

// Session is one live Relay connection.
type Session struct {
	connection *websocket.Conn
	ready      protocol.Ready
	events     chan protocol.Envelope
	errors     chan error
	cancel     context.CancelFunc
	writeMu    sync.Mutex
	close      sync.Once
}

// Dial opens a Relay session, completes hello, and starts the event reader.
func Dial(ctx context.Context, config Config) (*Session, error) {
	if config.URL == "" || config.DeviceID == "" || config.ClientVersion == "" || config.RulesVersion == "" {
		return nil, errors.New("relay URL, identity, client version, and rules version are required")
	}
	connection, response, err := websocket.Dial(ctx, config.URL, &websocket.DialOptions{
		HTTPClient:      http.DefaultClient,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if response != nil {
			return nil, &DialError{Code: "http_status", Message: fmt.Sprintf("connect relay: HTTP %d", response.StatusCode), Retryable: response.StatusCode >= 500, Cause: err}
		}
		return nil, &DialError{Code: "network", Message: "Could not reach Relay.", Retryable: true, Cause: err}
	}
	connection.SetReadLimit(protocol.MaxFrameBytes)
	sessionContext, cancel := context.WithCancel(context.Background())
	session := &Session{
		connection: connection,
		events:     make(chan protocol.Envelope, 64),
		errors:     make(chan error, 1),
		cancel:     cancel,
	}
	helloID, err := protocol.NewEventID()
	if err != nil {
		session.Close()
		return nil, err
	}
	hello, _ := protocol.New(protocol.EventHello, helloID, protocol.Hello{
		DeviceID: config.DeviceID, ClientVersion: config.ClientVersion,
		RulesVersion: config.RulesVersion, ResumeToken: config.ResumeToken,
	})
	if err := session.write(ctx, hello); err != nil {
		session.Close()
		return nil, err
	}
	envelope, err := session.read(ctx, true)
	if err != nil {
		session.Close()
		return nil, mapReadError(err)
	}
	if envelope.Type == protocol.EventError {
		problem, decodeErr := protocol.Payload[protocol.Error](envelope)
		if decodeErr != nil || problem.Validate() != nil {
			session.Close()
			return nil, mapReadError(invalidRelayEventError(errors.New("relay returned an invalid error event")))
		}
		session.Close()
		return nil, &DialError{Code: problem.Code, Message: problem.Message, Retryable: problem.Retryable}
	}
	if envelope.Type != protocol.EventReady {
		session.Close()
		return nil, mapReadError(invalidRelayEventError(fmt.Errorf("expected ready, received %s", envelope.Type)))
	}
	ready, err := protocol.Payload[protocol.Ready](envelope)
	if err != nil || ready.BuilderID <= 0 || ready.ResumeToken == "" {
		session.Close()
		return nil, mapReadError(invalidRelayEventError(errors.New("relay returned an invalid ready event")))
	}
	session.ready = ready
	go session.readLoop(sessionContext)
	return session, nil
}

// Ready returns the immutable handshake result.
func (session *Session) Ready() protocol.Ready { return session.ready }

// Events returns ordered Relay events until the session closes.
func (session *Session) Events() <-chan protocol.Envelope { return session.events }

// Errors reports the terminal read error, if one occurred.
func (session *Session) Errors() <-chan error { return session.errors }

// Send creates and writes one typed client event.
func (session *Session) Send(ctx context.Context, eventType protocol.EventType, payload any) (string, error) {
	id, err := protocol.NewEventID()
	if err != nil {
		return "", err
	}
	return id, session.SendWithID(ctx, eventType, id, payload)
}

// SendWithID writes one typed client event with a caller-owned ID. It is used
// when the application must correlate a later Relay error with one operation.
func (session *Session) SendWithID(ctx context.Context, eventType protocol.EventType, id string, payload any) error {
	envelope, err := protocol.New(eventType, id, payload)
	if err != nil {
		return err
	}
	if err := session.write(ctx, envelope); err != nil {
		return err
	}
	return nil
}

// Close stops local activity immediately. User-visible exits send their
// best-effort protocol event before this transport-level close.
func (session *Session) Close() error {
	var closeErr error
	session.close.Do(func() {
		session.cancel()
		closeErr = session.connection.CloseNow()
	})
	return closeErr
}

func (session *Session) readLoop(ctx context.Context) {
	defer close(session.events)
	defer close(session.errors)
	for {
		envelope, err := session.read(ctx, false)
		if err != nil {
			err = mapReadError(err)
			if ctx.Err() == nil && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				select {
				case session.errors <- err:
				default:
				}
			}
			return
		}
		select {
		case session.events <- envelope:
		case <-ctx.Done():
			return
		}
	}
}

func mapReadError(err error) error {
	if errors.Is(err, protocol.ErrUnsupportedVersion) {
		return &DialError{
			Code: "protocol_incompatible", Message: "Update Lobbyte to continue.",
			Retryable: false, Cause: err,
		}
	}
	if errors.Is(err, errInvalidRelayEvent) {
		return &DialError{
			Code: "invalid_relay_event", Message: "Relay sent an invalid event.",
			Retryable: false, Cause: err,
		}
	}
	return err
}

func (session *Session) read(ctx context.Context, allowReady bool) (protocol.Envelope, error) {
	messageType, data, err := session.connection.Read(ctx)
	if err != nil {
		return protocol.Envelope{}, err
	}
	if messageType != websocket.MessageText {
		return protocol.Envelope{}, invalidRelayEventError(errors.New("relay sent a binary event"))
	}
	envelope, err := protocol.Decode(data)
	if err != nil {
		if !errors.Is(err, protocol.ErrUnsupportedVersion) {
			err = invalidRelayEventError(err)
		}
		return protocol.Envelope{}, err
	}
	if envelope.Type == protocol.EventError {
		problem, err := protocol.Payload[protocol.Error](envelope)
		if err != nil || problem.Validate() != nil {
			return protocol.Envelope{}, invalidRelayEventError(errors.New("relay returned an invalid error event"))
		}
	}
	if !isRelayEvent(envelope.Type, allowReady) {
		return protocol.Envelope{}, invalidRelayEventError(fmt.Errorf("relay returned unexpected event type %q", envelope.Type))
	}
	return envelope, nil
}

func isRelayEvent(eventType protocol.EventType, allowReady bool) bool {
	switch eventType {
	case protocol.EventReady:
		return allowReady
	case protocol.EventMatchWaiting, protocol.EventRoomJoined, protocol.EventPresenceChanged,
		protocol.EventMessageReceived, protocol.EventRoomEnded, protocol.EventError:
		return true
	default:
		return false
	}
}

func invalidRelayEventError(err error) error {
	return &invalidRelayError{cause: err}
}

func (session *Session) write(ctx context.Context, envelope protocol.Envelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	writeContext, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if err := session.connection.Write(writeContext, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write relay event: %w", err)
	}
	return nil
}
