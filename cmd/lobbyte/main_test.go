package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMalformedInternalHookInvocationIsSilentAndHandled(t *testing.T) {
	t.Parallel()

	stdin := bytes.NewBufferString("sensitive hook payload")
	if !runHookCommand([]string{"lobbyte", "hook"}, stdin) {
		t.Fatal("hook command was not handled")
	}
	if stdin.Len() != 0 {
		t.Fatalf("hook stdin was not drained: %d bytes remain", stdin.Len())
	}
	if runHookCommand([]string{"lobbyte", "hooks", "remove"}, bytes.NewReader(nil)) {
		t.Fatal("public maintenance command was treated as an internal hook")
	}
}

func TestVersionOutputUsesInjectedClientVersion(t *testing.T) {
	original := clientVersion
	t.Cleanup(func() { clientVersion = original })
	clientVersion = "0.1.0-test"

	if got := versionText(); got != "lobbyte 0.1.0-test\n" {
		t.Fatalf("version text = %q", got)
	}
}

func TestProfileOverrideIsLimitedToDevelopmentBuilds(t *testing.T) {
	originalVersion := clientVersion
	t.Cleanup(func() { clientVersion = originalVersion })
	t.Setenv("HOME", t.TempDir())
	override := filepath.Join(t.TempDir(), "profile")
	t.Setenv("LOBBYTE_DEV_PROFILE", override)

	clientVersion = "dev"
	profile, err := clientProfileDir()
	if err != nil || profile != override {
		t.Fatalf("development profile=%q err=%v", profile, err)
	}
	clientVersion = "0.1.0"
	profile, err = clientProfileDir()
	if err != nil {
		t.Fatal(err)
	}
	if profile == override || filepath.Base(profile) != "Lobbyte" {
		t.Fatalf("release accepted development profile: %q", profile)
	}
	if _, err := os.Stat(profile); err == nil {
		t.Fatal("profile lookup unexpectedly created a directory")
	}
}

func TestRelayAndRuntimeOverridesAreLimitedToDevelopmentBuilds(t *testing.T) {
	originalVersion, originalRelayURL := clientVersion, relayURL
	t.Cleanup(func() { clientVersion, relayURL = originalVersion, originalRelayURL })
	overrideProfile := filepath.Join(t.TempDir(), "profile")
	overrideRelay := "ws://127.0.0.1:18787/v1/ws"
	t.Setenv("LOBBYTE_DEV_PROFILE", overrideProfile)
	t.Setenv("LOBBYTE_RELAY_URL", overrideRelay)
	relayURL = "wss://relay.lobbyte.online/v1/ws"

	clientVersion = "dev"
	profile, err := clientProfileDir()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := clientRuntimeDir(profile)
	if err != nil {
		t.Fatal(err)
	}
	if got := clientRelayURL(); got != overrideRelay {
		t.Fatalf("development Relay URL = %q", got)
	}
	if runtime != filepath.Join(overrideProfile, "runtime") {
		t.Fatalf("development runtime = %q", runtime)
	}

	clientVersion = "0.1.0"
	profile, err = clientProfileDir()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = clientRuntimeDir(profile)
	if err != nil {
		t.Fatal(err)
	}
	if got := clientRelayURL(); got != relayURL {
		t.Fatalf("release Relay URL = %q, want %q", got, relayURL)
	}
	if runtime == filepath.Join(overrideProfile, "runtime") || filepath.Dir(runtime) != filepath.Join(mustUserCacheDir(t), "Lobbyte") {
		t.Fatalf("release runtime accepted development override: %q", runtime)
	}
}

func mustUserCacheDir(t *testing.T) string {
	t.Helper()
	directory, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
