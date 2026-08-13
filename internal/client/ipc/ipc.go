// Package ipc owns single-instance coordination and local hook event delivery.
package ipc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/xfey/lobbyte/internal/protocol"
	"golang.org/x/sys/unix"
)

const DescriptorVersion = 1

var ErrAlreadyRunning = errors.New("Lobbyte is already running")

// Descriptor is the current TUI's user-private loopback endpoint.
type Descriptor struct {
	Version int    `json:"version"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
}

// Event contains only the allowlisted source and lifecycle event.
type Event struct {
	Source protocol.AgentSource `json:"source"`
	Name   string               `json:"event"`
}

// Server owns the single-instance lock, descriptor and loopback endpoint.
type Server struct {
	root       string
	lock       *os.File
	descriptor Descriptor
	http       *http.Server
	listener   net.Listener
	events     chan Event
	close      sync.Once
}

// Start acquires the profile lock and starts a random loopback HTTP endpoint.
func Start(root string) (*Server, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("IPC root must be absolute")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create IPC directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect IPC directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(root, "instance.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("acquire instance lock: %w", err)
	}
	if err := lock.Chmod(0o600); err != nil {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
		return nil, fmt.Errorf("protect instance lock: %w", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("listen for hook events: %w", err)
	}
	token, err := randomToken(32)
	if err != nil {
		listener.Close()
		lock.Close()
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	server := &Server{
		root: root, lock: lock, listener: listener, events: make(chan Event, 32),
		descriptor: Descriptor{Version: DescriptorVersion, Port: port, Token: token, PID: os.Getpid()},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/event", server.receive)
	server.http = &http.Server{
		Handler: mux, ReadHeaderTimeout: time.Second, IdleTimeout: time.Second,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	if err := writeDescriptor(filepath.Join(root, "runtime.json"), server.descriptor); err != nil {
		listener.Close()
		lock.Close()
		return nil, err
	}
	go server.http.Serve(listener) //nolint:errcheck
	return server, nil
}

// Events returns allowlisted hook events in arrival order.
func (server *Server) Events() <-chan Event { return server.events }

// Descriptor returns a copy of the private endpoint metadata.
func (server *Server) Descriptor() Descriptor { return server.descriptor }

// Close removes runtime state and releases the single-instance lock.
func (server *Server) Close() error {
	var closeErr error
	server.close.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		closeErr = server.http.Shutdown(ctx)
		_ = os.Remove(filepath.Join(server.root, "runtime.json"))
		_ = unix.Flock(int(server.lock.Fd()), unix.LOCK_UN)
		_ = server.lock.Close()
		close(server.events)
	})
	return closeErr
}

func (server *Server) receive(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, 1024)
	if request.Header.Get("Authorization") != "Bearer "+server.descriptor.Token {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	var event Event
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil || validateEvent(event) != nil || !decoderAtEOF(decoder) {
		http.Error(writer, "invalid event", http.StatusBadRequest)
		return
	}
	select {
	case server.events <- event:
		writer.WriteHeader(http.StatusNoContent)
	default:
		http.Error(writer, "busy", http.StatusServiceUnavailable)
	}
}

func decoderAtEOF(decoder *json.Decoder) bool {
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

// SendHook drains stdin without parsing it, then best-effort delivers metadata.
func SendHook(ctx context.Context, root string, source protocol.AgentSource, eventName string, stdin io.Reader) {
	drainDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		close(drainDone)
	}()
	defer func() {
		select {
		case <-drainDone:
		case <-ctx.Done():
		}
	}()
	if validateEvent(Event{Source: source, Name: eventName}) != nil {
		return
	}
	if !filepath.IsAbs(root) {
		return
	}
	descriptor, err := readDescriptor(filepath.Join(root, "runtime.json"))
	if err != nil {
		return
	}
	data, _ := json.Marshal(Event{Source: source, Name: eventName})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://127.0.0.1:"+strconv.Itoa(descriptor.Port)+"/v1/event", bytes.NewReader(data))
	if err != nil {
		return
	}
	request.Header.Set("Authorization", "Bearer "+descriptor.Token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 300 * time.Millisecond}
	response, err := client.Do(request)
	if err == nil {
		response.Body.Close()
	}
	select {
	case <-drainDone:
	case <-ctx.Done():
	}
}

func validateEvent(event Event) error {
	if event.Source != protocol.SourceCodex && event.Source != protocol.SourceClaudeCode {
		return errors.New("unsupported agent source")
	}
	switch event.Name {
	case "SessionStart", "UserPromptSubmit", "Stop", "SessionEnd":
		return nil
	default:
		return errors.New("unsupported hook event")
	}
}

func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate IPC token: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func writeDescriptor(path string, descriptor Descriptor) error {
	data, _ := json.Marshal(descriptor)
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".runtime-*")
	if err != nil {
		return fmt.Errorf("create runtime descriptor: %w", err)
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open IPC directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync IPC directory: %w", err)
	}
	return nil
}

func readDescriptor(path string) (Descriptor, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Descriptor{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return Descriptor{}, errors.New("runtime descriptor is not a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Descriptor{}, err
	}
	var descriptor Descriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		return Descriptor{}, err
	}
	token, tokenErr := hex.DecodeString(descriptor.Token)
	if descriptor.Version != DescriptorVersion || descriptor.Port <= 0 || descriptor.Port > 65_535 ||
		descriptor.PID <= 0 || tokenErr != nil || len(token) != 32 {
		return Descriptor{}, errors.New("invalid runtime descriptor")
	}
	return descriptor, nil
}
