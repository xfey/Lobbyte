package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xfey/lobbyte/internal/protocol"
)

func TestSingleInstanceAndHookDelivery(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	server, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := Start(root); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	SendHook(ctx, root, protocol.SourceCodex, "UserPromptSubmit", bytes.NewBufferString(`{"prompt":"secret"}`))
	select {
	case event := <-server.Events():
		if event.Source != protocol.SourceCodex || event.Name != "UserPromptSubmit" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("hook event was not delivered")
	}
}

func TestDescriptorIsPrivateAndRemoved(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	server, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runtime.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("descriptor mode = %o", info.Mode().Perm())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime descriptor still exists: %v", err)
	}
}

func TestReadDescriptorRejectsUnsafeMetadata(t *testing.T) {
	t.Parallel()

	valid := Descriptor{Version: DescriptorVersion, Port: 1234, Token: strings.Repeat("ab", 32), PID: 42}
	for _, test := range []struct {
		name       string
		descriptor Descriptor
		mode       os.FileMode
	}{
		{name: "public file", descriptor: valid, mode: 0o644},
		{name: "bad version", descriptor: Descriptor{Version: 2, Port: 1234, Token: valid.Token, PID: 42}, mode: 0o600},
		{name: "bad port", descriptor: Descriptor{Version: 1, Port: 65_536, Token: valid.Token, PID: 42}, mode: 0o600},
		{name: "bad token", descriptor: Descriptor{Version: 1, Port: 1234, Token: "not-a-token", PID: 42}, mode: 0o600},
		{name: "bad pid", descriptor: Descriptor{Version: 1, Port: 1234, Token: valid.Token}, mode: 0o600},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.json")
			data, _ := json.Marshal(test.descriptor)
			if err := os.WriteFile(path, data, test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := readDescriptor(path); err == nil {
				t.Fatal("readDescriptor accepted unsafe metadata")
			}
		})
	}

	path := filepath.Join(t.TempDir(), "runtime.json")
	data, _ := json.Marshal(valid)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if descriptor, err := readDescriptor(path); err != nil || descriptor != valid {
		t.Fatalf("valid descriptor = %#v, err = %v", descriptor, err)
	}
}

func TestHookEndpointIsLoopbackOnly(t *testing.T) {
	t.Parallel()

	server, err := Start(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	address, ok := server.listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() || address.IP.String() != "127.0.0.1" {
		t.Fatalf("Hook endpoint address = %#v", server.listener.Addr())
	}
}

func TestSendHookStopsAtIPCDeadline(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	httpServer := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(time.Second)
	})}
	defer httpServer.Close()
	go httpServer.Serve(listener) //nolint:errcheck

	root := t.TempDir()
	descriptor := Descriptor{
		Version: DescriptorVersion, Port: listener.Addr().(*net.TCPAddr).Port,
		Token: "test-token", PID: os.Getpid(),
	}
	if err := writeDescriptor(filepath.Join(root, "runtime.json"), descriptor); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	SendHook(context.Background(), root, protocol.SourceCodex, "Stop", bytes.NewReader(nil))
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("SendHook took %v, want the approximately 300ms IPC bound", elapsed)
	}
}

func TestStartRepairsExistingRuntimeDirectoryPermissions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	server, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime directory mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestStartRepairsExistingInstanceLockPermissions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	lockPath := filepath.Join(root, "instance.lock")
	if err := os.WriteFile(lockPath, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	server, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(lockPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("instance lock mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestUnknownEventIsSilentlyIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	server, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	SendHook(context.Background(), root, protocol.SourceCodex, "PreToolUse", bytes.NewBufferString("secret"))
	select {
	case event := <-server.Events():
		t.Fatalf("unexpected event: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRelativeRuntimePathIsSilentlyIgnored(t *testing.T) {
	t.Parallel()

	SendHook(context.Background(), "relative-runtime", protocol.SourceCodex, "Stop", bytes.NewReader(nil))
}

func TestForgedTokenIsRejectedWithoutDeliveringEvent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	server, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	body, _ := json.Marshal(Event{Source: protocol.SourceCodex, Name: "Stop"})
	request, _ := http.NewRequest(http.MethodPost,
		"http://127.0.0.1:"+strconv.Itoa(server.Descriptor().Port)+"/v1/event", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer forged")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged token status = %d", response.StatusCode)
	}
	select {
	case event := <-server.Events():
		t.Fatalf("forged event delivered: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestHookEndpointRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	server, err := Start(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	body := `{"source":"codex","event":"Stop"}{"prompt":"secret"}`
	request, _ := http.NewRequest(http.MethodPost,
		"http://127.0.0.1:"+strconv.Itoa(server.Descriptor().Port)+"/v1/event", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+server.Descriptor().Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d", response.StatusCode)
	}
	select {
	case event := <-server.Events():
		t.Fatalf("trailing event delivered: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestStartReplacesStaleDescriptorAfterCleanLockAcquisition(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stale := []byte(`{"version":1,"port":1,"token":"stale","pid":1}\n`)
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if server.Descriptor().Token == "stale" || server.Descriptor().Port == 1 {
		t.Fatalf("stale descriptor was reused: %#v", server.Descriptor())
	}
}

func TestLargeSensitiveStdinIsDrainedAndNeverDelivered(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	server, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	payload := bytes.Repeat([]byte(`{"prompt":"PRIVATE_SECRET","path":"/private/repo"}`), 256*1024)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	SendHook(ctx, root, protocol.SourceClaudeCode, "Stop", bytes.NewReader(payload))
	select {
	case event := <-server.Events():
		if event != (Event{Source: protocol.SourceClaudeCode, Name: "Stop"}) {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("large stdin prevented metadata delivery")
	}
	files, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, _ := os.ReadFile(filepath.Join(root, file.Name()))
		if bytes.Contains(data, []byte("PRIVATE_SECRET")) {
			t.Fatalf("sensitive stdin persisted in %s", file.Name())
		}
	}
}
