package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xfey/lobbyte/internal/client/app"
	"github.com/xfey/lobbyte/internal/client/hooks"
	"github.com/xfey/lobbyte/internal/client/ipc"
	"github.com/xfey/lobbyte/internal/protocol"
)

var (
	clientVersion = "dev"
	rulesVersion  = "candidate-1"
	rulesURL      = ""
	privacyURL    = ""
	relayURL      = "wss://relay.lobbyte.online/v1/ws"
)

func main() {
	if runHookCommand(os.Args, os.Stdin) {
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Print(versionText())
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	profileDir, err := clientProfileDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fail(err)
	}
	executable, err := app.ResolveExecutable()
	if err != nil {
		fail(err)
	}
	runtimeDir, err := clientRuntimeDir(profileDir)
	if err != nil {
		fail(err)
	}
	if len(os.Args) > 1 {
		switch {
		case len(os.Args) == 3 && os.Args[1] == "hooks" && os.Args[2] == "remove":
			if err := (hooks.Manager{
				Home: homeDir, Executable: executable, RuntimeDir: runtimeDir,
			}).RemoveAll(); err != nil {
				fail(err)
			}
			return
		default:
			fail(fmt.Errorf("usage: lobbyte [--version] | lobbyte hooks remove"))
		}
	}
	processRelayURL := clientRelayURL()
	if err := app.Run(ctx, os.Stdout, app.Config{
		ProfileDir: profileDir, RuntimeDir: runtimeDir, RelayURL: processRelayURL,
		ClientVersion: clientVersion, RulesVersion: rulesVersion,
		RulesURL: rulesURL, PrivacyURL: privacyURL,
		HomeDir: homeDir, Executable: executable, Input: os.Stdin,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func versionText() string { return fmt.Sprintf("lobbyte %s\n", clientVersion) }

func runHookCommand(arguments []string, stdin io.Reader) bool {
	if len(arguments) < 2 || arguments[1] != "hook" {
		return false
	}
	source, event, runtimeDir := protocol.AgentSource(""), "", ""
	if len(arguments) == 5 {
		source, event, runtimeDir = protocol.AgentSource(arguments[2]), arguments[3], arguments[4]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ipc.SendHook(ctx, runtimeDir, source, event, stdin)
	return true
}

func clientRuntimeDir(profileDir string) (string, error) {
	if developmentProfileEnabled() {
		return filepath.Join(profileDir, "runtime"), nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "Lobbyte", "runtime"), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func clientProfileDir() (string, error) {
	if override := os.Getenv("LOBBYTE_DEV_PROFILE"); override != "" && developmentProfileEnabled() {
		return filepath.Abs(override)
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(configDir, "Lobbyte"), nil
}

func clientRelayURL() string {
	if override := os.Getenv("LOBBYTE_RELAY_URL"); override != "" && developmentProfileEnabled() {
		return override
	}
	return relayURL
}

func developmentProfileEnabled() bool {
	return clientVersion == "dev" && os.Getenv("LOBBYTE_DEV_PROFILE") != ""
}
