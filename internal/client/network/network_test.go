package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/xfey/lobbyte/internal/protocol"
)

func TestTransportErrorsRemainRetryableByTheApplication(t *testing.T) {
	t.Parallel()

	if mapped := mapReadError(io.EOF); mapped != io.EOF {
		t.Fatalf("transport EOF was reclassified: %#v", mapped)
	}
	if !errors.Is(invalidRelayEventError(protocol.ErrUnsupportedVersion), protocol.ErrUnsupportedVersion) {
		t.Fatal("invalid Relay wrapper did not preserve its cause")
	}
	var problem *DialError
	if mapped := mapReadError(invalidRelayEventError(errors.New("bad payload"))); !errors.As(mapped, &problem) || problem.Code != "invalid_relay_event" || problem.Retryable {
		t.Fatalf("invalid Relay event mapping = %#v", mapped)
	}
}

func TestDialAndExchangeEvents(t *testing.T) {
	t.Parallel()

	url := testRelayURL(t, func(ctx context.Context, connection *websocket.Conn, hello protocol.Envelope) {
		payload, err := protocol.Payload[protocol.Hello](hello)
		if err != nil || payload.RulesVersion != "dev-1" {
			return
		}
		writeTestEnvelope(ctx, connection, protocol.EventReady, protocol.Ready{
			BuilderID: 7, ResumeToken: "test-token",
		})
		_, data, err := connection.Read(ctx)
		if err != nil {
			return
		}
		event, err := protocol.Decode(data)
		if err != nil || event.Type != protocol.EventMatchJoin {
			return
		}
		writeTestEnvelope(ctx, connection, protocol.EventMatchWaiting, struct{}{})
	})
	session, err := Dial(context.Background(), Config{
		URL: url, DeviceID: uuid.NewString(), ClientVersion: "test", RulesVersion: "dev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.Ready().BuilderID != 7 {
		t.Fatalf("Builder ID = %d", session.Ready().BuilderID)
	}
	if _, err := session.Send(context.Background(), protocol.EventMatchJoin, protocol.MatchJoin{IntentID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	readType(t, session, protocol.EventMatchWaiting)
}

func TestDialRejectsOldRules(t *testing.T) {
	t.Parallel()

	url := testRelayURL(t, func(ctx context.Context, connection *websocket.Conn, _ protocol.Envelope) {
		writeTestEnvelope(ctx, connection, protocol.EventError, protocol.Error{
			Code: "rules_outdated", Message: "Update Lobbyte to continue.", Retryable: false,
		})
	})
	_, err := Dial(context.Background(), Config{
		URL: url, DeviceID: uuid.NewString(), ClientVersion: "test", RulesVersion: "old",
	})
	if err == nil || !strings.Contains(err.Error(), "Update Lobbyte") {
		t.Fatalf("Dial error = %v", err)
	}
}

func TestDialMapsUnsupportedRelayProtocolToNonRetryableUpgrade(t *testing.T) {
	t.Parallel()

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
		_ = connection.Write(ctx, websocket.MessageText, []byte(`{"v":2,"type":"ready","id":"future","data":{}}`))
	}))
	defer httpServer.Close()

	_, err := Dial(context.Background(), Config{
		URL: "ws" + strings.TrimPrefix(httpServer.URL, "http"), DeviceID: uuid.NewString(),
		ClientVersion: "test", RulesVersion: "dev-1",
	})
	var problem *DialError
	if !errors.As(err, &problem) || problem.Code != "protocol_incompatible" || problem.Retryable ||
		problem.Message != "Update Lobbyte to continue." {
		t.Fatalf("Dial error = %#v", err)
	}
}

func TestDialRejectsInvalidReadyAsNonRetryableRelayFailure(t *testing.T) {
	t.Parallel()

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
		invalid, _ := protocol.New(protocol.EventReady, "ready", protocol.Ready{BuilderID: 0, ResumeToken: ""})
		data, _ := json.Marshal(invalid)
		_ = connection.Write(ctx, websocket.MessageText, data)
	}))
	defer httpServer.Close()

	_, err := Dial(context.Background(), Config{
		URL: "ws" + strings.TrimPrefix(httpServer.URL, "http"), DeviceID: uuid.NewString(),
		ClientVersion: "test", RulesVersion: "dev-1",
	})
	var problem *DialError
	if !errors.As(err, &problem) || problem.Code != "invalid_relay_event" || problem.Retryable {
		t.Fatalf("invalid ready error = %#v", err)
	}
}

func TestSessionMapsFutureProtocolEventToNonRetryableUpgrade(t *testing.T) {
	t.Parallel()

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
		ready, _ := protocol.New(protocol.EventReady, "ready", protocol.Ready{BuilderID: 1, ResumeToken: "token"})
		readyData, _ := json.Marshal(ready)
		if err := connection.Write(ctx, websocket.MessageText, readyData); err != nil {
			return
		}
		_ = connection.Write(ctx, websocket.MessageText, []byte(`{"v":2,"type":"match.waiting","id":"future","data":{}}`))
		<-request.Context().Done()
	}))
	defer httpServer.Close()

	session, err := Dial(context.Background(), Config{
		URL: "ws" + strings.TrimPrefix(httpServer.URL, "http"), DeviceID: uuid.NewString(),
		ClientVersion: "test", RulesVersion: "dev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	select {
	case err := <-session.Errors():
		var problem *DialError
		if !errors.As(err, &problem) || problem.Code != "protocol_incompatible" || problem.Retryable {
			t.Fatalf("session error = %#v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for future protocol error")
	}
}

func TestSessionRejectsUnsafeRelayErrorWithoutRetrying(t *testing.T) {
	t.Parallel()

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
		ready, _ := protocol.New(protocol.EventReady, "ready", protocol.Ready{BuilderID: 1, ResumeToken: "token"})
		readyData, _ := json.Marshal(ready)
		if err := connection.Write(ctx, websocket.MessageText, readyData); err != nil {
			return
		}
		_ = connection.Write(ctx, websocket.MessageText, []byte(
			`{"v":1,"type":"error","id":"error","data":{"code":"operation_failed","message":"unsafe\u001b[31m","retryable":true}}`))
		<-request.Context().Done()
	}))
	defer httpServer.Close()

	session, err := Dial(context.Background(), Config{
		URL: "ws" + strings.TrimPrefix(httpServer.URL, "http"), DeviceID: uuid.NewString(),
		ClientVersion: "test", RulesVersion: "dev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	select {
	case err := <-session.Errors():
		var problem *DialError
		if !errors.As(err, &problem) || problem.Code != "invalid_relay_event" || problem.Retryable {
			t.Fatalf("unsafe Relay error = %#v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for invalid Relay error")
	}
}

func TestSessionRejectsClientOnlyEventFromRelayWithoutRetrying(t *testing.T) {
	t.Parallel()

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
		ready, _ := protocol.New(protocol.EventReady, "ready", protocol.Ready{BuilderID: 1, ResumeToken: "token"})
		readyData, _ := json.Marshal(ready)
		if err := connection.Write(ctx, websocket.MessageText, readyData); err != nil {
			return
		}
		unexpected, _ := protocol.New(protocol.EventMessageSend, "unexpected", protocol.MessageSend{
			RoomID: "room", Text: "not a Relay event",
		})
		unexpectedData, _ := json.Marshal(unexpected)
		_ = connection.Write(ctx, websocket.MessageText, unexpectedData)
		<-request.Context().Done()
	}))
	defer httpServer.Close()

	session, err := Dial(context.Background(), Config{
		URL: "ws" + strings.TrimPrefix(httpServer.URL, "http"), DeviceID: uuid.NewString(),
		ClientVersion: "test", RulesVersion: "dev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	select {
	case err := <-session.Errors():
		var problem *DialError
		if !errors.As(err, &problem) || problem.Code != "invalid_relay_event" || problem.Retryable {
			t.Fatalf("unexpected Relay event error = %#v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unexpected Relay event error")
	}
}

func TestSessionRejectsSecondReadyEventWithoutRetrying(t *testing.T) {
	t.Parallel()

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
		ready, _ := protocol.New(protocol.EventReady, "ready", protocol.Ready{BuilderID: 1, ResumeToken: "token"})
		readyData, _ := json.Marshal(ready)
		if err := connection.Write(ctx, websocket.MessageText, readyData); err != nil {
			return
		}
		second, _ := protocol.New(protocol.EventReady, "second", protocol.Ready{BuilderID: 2, ResumeToken: "other"})
		secondData, _ := json.Marshal(second)
		_ = connection.Write(ctx, websocket.MessageText, secondData)
		<-request.Context().Done()
	}))
	defer httpServer.Close()

	session, err := Dial(context.Background(), Config{
		URL: "ws" + strings.TrimPrefix(httpServer.URL, "http"), DeviceID: uuid.NewString(),
		ClientVersion: "test", RulesVersion: "dev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	select {
	case err := <-session.Errors():
		var problem *DialError
		if !errors.As(err, &problem) || problem.Code != "invalid_relay_event" || problem.Retryable {
			t.Fatalf("second ready error = %#v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second ready error")
	}
}

func testRelayURL(t *testing.T, handle func(context.Context, *websocket.Conn, protocol.Envelope)) string {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		messageType, data, err := connection.Read(ctx)
		if err != nil || messageType != websocket.MessageText {
			return
		}
		hello, err := protocol.Decode(data)
		if err != nil || hello.Type != protocol.EventHello {
			return
		}
		handle(ctx, connection, hello)
	}))
	t.Cleanup(httpServer.Close)
	return "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

func writeTestEnvelope(ctx context.Context, connection *websocket.Conn, eventType protocol.EventType, payload any) {
	event, err := protocol.New(eventType, "test-event", payload)
	if err != nil {
		return
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_ = connection.Write(ctx, websocket.MessageText, data)
}

func readType(t *testing.T, session *Session, eventType protocol.EventType) protocol.Envelope {
	t.Helper()
	select {
	case event := <-session.Events():
		if event.Type != eventType {
			t.Fatalf("event type = %s, want %s", event.Type, eventType)
		}
		return event
	case err := <-session.Errors():
		t.Fatalf("session error: %v", err)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", eventType)
	}
	return protocol.Envelope{}
}
