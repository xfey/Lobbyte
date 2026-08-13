package hooks

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSetupPreservesExistingHooksAndRemoveIsExact(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	for _, directory := range []string{".codex", ".claude"} {
		if err := os.MkdirAll(filepath.Join(home, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	existing := map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "user-command"}}},
	}}}
	writeJSON(t, filepath.Join(home, ".claude", "settings.json"), existing)
	manager := Manager{Home: home, Executable: "/Applications/Lobbyte App/lobbyte", RuntimeDir: filepath.Join(home, "runtime"), AgentAvailable: testAgentAvailable}
	result, present := manager.Setup(ClaudeCode)
	if !present || result.Status != "Configured" {
		t.Fatalf("Setup result = %#v, present = %v", result, present)
	}
	object := readJSON(t, filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(mustJSON(object), "user-command") || !strings.Contains(mustJSON(object), "Lobbyte") {
		t.Fatalf("config did not preserve and add handlers: %#v", object)
	}
	if err := manager.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	object = readJSON(t, filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(mustJSON(object), "user-command") || strings.Contains(mustJSON(object), "Lobbyte") {
		t.Fatalf("remove was not exact: %#v", object)
	}
}

func TestSetupIsIdempotentAndExcludesCompaction(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Home: home, Executable: "/tmp/lobbyte", RuntimeDir: filepath.Join(home, "runtime"), AgentAvailable: testAgentAvailable}
	for range 2 {
		result, present := manager.Setup(Codex)
		if !present || result.Status != "Configured" || result.Reason != "review required" {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, `"type": "command"`) != 4 || strings.Contains(text, "statusMessage") || strings.Contains(text, "compact") {
		t.Fatalf("unexpected Codex config: %s", text)
	}
}

func TestClaudeUsesCompatibleShellFormAndPreservesModeAndBackupLimit(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	directory := filepath.Join(home, ".claude")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Home: home, Executable: "/Applications/Lobbyte App/lobbyte", RuntimeDir: filepath.Join(home, "runtime"), AgentAvailable: testAgentAvailable}
	for index := 0; index < 5; index++ {
		manager.Executable = "/Applications/Lobbyte App/lobbyte-" + string(rune('a'+index))
		result, _ := manager.Setup(ClaudeCode)
		if result.Status != "Configured" {
			t.Fatalf("setup result: %#v", result)
		}
	}
	object := readJSON(t, path)
	encoded := mustJSON(object)
	if !strings.Contains(encoded, `"command":"exec /bin/sh -c `) || strings.Contains(encoded, `"args"`) {
		t.Fatalf("Claude handler is not compatible shell form: %s", encoded)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("config mode = %v, err = %v", info.Mode().Perm(), err)
	}
	backups, err := filepath.Glob(path + ".lobbyte.*.bak")
	if err != nil || len(backups) != 3 {
		t.Fatalf("backup count = %d, err = %v", len(backups), err)
	}
	if strings.Count(encoded, `"statusMessage":"Lobbyte"`) != 4 || strings.Contains(encoded, "lobbyte-a") {
		t.Fatalf("old executable handlers were not replaced exactly: %s", encoded)
	}
	if !strings.Contains(encoded, `^(startup|resume|clear|fork)$`) {
		t.Fatalf("Claude SessionStart matcher is invalid: %s", encoded)
	}
	if err := manager.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	removed := mustJSON(readJSON(t, path))
	if strings.Contains(removed, "Lobbyte") {
		t.Fatalf("owned handlers remained after path change: %s", removed)
	}
}

func TestAgentSpecificSessionStartMatchersExcludeCompaction(t *testing.T) {
	t.Parallel()

	if got := eventMatcher(Codex, "SessionStart"); got != "^(startup|resume|clear)$" {
		t.Fatalf("Codex matcher = %q", got)
	}
	if got := eventMatcher(ClaudeCode, "SessionStart"); got != "^(startup|resume|clear|fork)$" {
		t.Fatalf("Claude matcher = %q", got)
	}
	for _, agent := range []Agent{Codex, ClaudeCode} {
		if strings.Contains(eventMatcher(agent, "SessionStart"), "compact") {
			t.Fatalf("%s matcher includes compaction", agent)
		}
	}
}

func TestEveryHandlerHasOneSecondTimeout(t *testing.T) {
	t.Parallel()

	for _, agent := range []Agent{Codex, ClaudeCode} {
		for _, event := range hookEvents {
			configured := handler("/tmp/lobbyte", "/tmp/lobbyte-runtime", agent, event)
			if configured["timeout"] != float64(1) {
				t.Fatalf("%s %s timeout = %#v", agent, event, configured["timeout"])
			}
		}
	}
}

func TestEveryHandlerExplainsItsLocalPrivacyBoundary(t *testing.T) {
	t.Parallel()

	for _, agent := range []Agent{Codex, ClaudeCode} {
		for _, event := range hookEvents {
			configured := handler("/tmp/lobbyte", "/tmp/lobbyte-runtime", agent, event)
			command, ok := configured["command"].(string)
			if !ok || !strings.Contains(command, hookCommandDescription+"\n") {
				t.Fatalf("%s %s command has no explanation: %#v", agent, event, configured["command"])
			}
			if strings.Count(command, hookCommandDescription) != 1 {
				t.Fatalf("%s %s command explanation count = %d", agent, event, strings.Count(command, hookCommandDescription))
			}
		}
	}
}

func TestShellTemplateExecutesQuotedPathAndMissingBinaryDrainsStdin(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "Lobbyte's App")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "lobbyte")
	marker := filepath.Join(directory, "called")
	script := "#!/bin/sh\nprintf '%s %s' \"$2\" \"$3\" >" + shellQuote(marker) + "\ncat >/dev/null\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		executable string
		wantMarker bool
	}{
		{name: "present", executable: executable, wantMarker: true},
		{name: "missing", executable: filepath.Join(directory, "missing")},
	} {
		t.Run(test.name, func(t *testing.T) {
			_ = os.Remove(marker)
			command := exec.Command("/bin/sh", "-c", shellCommand(test.executable, directory, ClaudeCode, "Stop"))
			command.Stdin = io.LimitReader(strings.NewReader(strings.Repeat("sensitive", 1_000_000)), 8_000_000)
			if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
				t.Fatalf("template output=%q error=%v", output, err)
			}
			_, err := os.Stat(marker)
			if test.wantMarker && err != nil {
				t.Fatalf("hook was not called: %v", err)
			}
			if test.wantMarker {
				data, readErr := os.ReadFile(marker)
				if readErr != nil || string(data) != "claude-code Stop" {
					t.Fatalf("hook arguments = %q, error=%v", data, readErr)
				}
			}
			if !test.wantMarker && !os.IsNotExist(err) {
				t.Fatalf("missing hook wrote marker: %v", err)
			}
		})
	}
}

func TestDisabledPolicyIsNotModified(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	directory := filepath.Join(home, ".claude")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "settings.json")
	data := []byte("{\"disableAllHooks\":true}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, _ := (Manager{Home: home, Executable: "/tmp/lobbyte", RuntimeDir: filepath.Join(home, "runtime"), AgentAvailable: testAgentAvailable}).Setup(ClaudeCode)
	if result.Status != "Disabled" || result.Reason != "hooks disabled" {
		t.Fatalf("unexpected result: %#v", result)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("disabled config was modified")
	}
}

func TestSetupSilentlySkipsStaleConfigForUnavailableAgent(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Home: home, Executable: "/tmp/lobbyte", RuntimeDir: filepath.Join(home, "runtime"),
		AgentAvailable: func(Agent) bool { return false },
	}
	if result, present := manager.Setup(Codex); present || result != (Result{}) {
		t.Fatalf("unavailable agent result=%#v present=%v", result, present)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unavailable agent config was written: %v", err)
	}
}

func TestCodexPolicyUsesCanonicalTOMLScopes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		config string
		reason string
	}{
		{name: "canonical feature", config: "[features]\nhooks = false\n", reason: "hooks disabled"},
		{name: "deprecated feature", config: "[features]\ncodex_hooks = false # compatibility\n", reason: "hooks disabled"},
		{name: "managed only", config: "allow_managed_hooks_only = true\n[features]\nhooks = true\n", reason: "managed hooks only"},
		{name: "unrelated table", config: "[plugin]\nhooks = false\nallow_managed_hooks_only = true\n"},
		{name: "commented and quoted", config: "# hooks = false\nlabel = 'allow_managed_hooks_only = true # text'\n[features]\nhooks = true\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			disabled, reason := codexPolicy(path)
			if disabled != (test.reason != "") || reason != test.reason {
				t.Fatalf("codexPolicy() = %v, %q; want %v, %q", disabled, reason, test.reason != "", test.reason)
			}
		})
	}
}

func TestInvalidJSONIsNotOverwritten(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	directory := filepath.Join(home, ".codex")
	_ = os.Mkdir(directory, 0o700)
	path := filepath.Join(directory, "hooks.json")
	_ = os.WriteFile(path, []byte("not json"), 0o600)
	result, _ := (Manager{Home: home, Executable: "/tmp/lobbyte", RuntimeDir: filepath.Join(home, "runtime"), AgentAvailable: testAgentAvailable}).Setup(Codex)
	if result.Status != "Disabled" || string(mustRead(t, path)) != "not json" {
		t.Fatalf("invalid config handling failed: %#v", result)
	}
}

func TestIncompatibleJSONStructureIsNotOverwritten(t *testing.T) {
	t.Parallel()

	for _, original := range []string{
		"null\n",
		`{"hooks":"owned by a future schema"}` + "\n",
		`{"hooks":{"Stop":{"command":"user-command"}}}` + "\n",
	} {
		home := t.TempDir()
		directory := filepath.Join(home, ".codex")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "hooks.json")
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		result, present := (Manager{
			Home: home, Executable: "/tmp/lobbyte", RuntimeDir: filepath.Join(home, "runtime"),
			AgentAvailable: testAgentAvailable,
		}).Setup(Codex)
		if !present || result.Status != "Disabled" || string(mustRead(t, path)) != original {
			t.Fatalf("incompatible config handling: result=%#v content=%q", result, mustRead(t, path))
		}
	}
}

func TestConcurrentSetupConvergesToOneHandlerSet(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	directory := filepath.Join(home, ".claude")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Home: home, Executable: "/tmp/lobbyte", RuntimeDir: filepath.Join(home, "runtime"), AgentAvailable: testAgentAvailable}
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, present := manager.Setup(ClaudeCode)
			if !present || result.Status != "Configured" {
				t.Errorf("concurrent setup result=%#v present=%v", result, present)
			}
		}()
	}
	group.Wait()
	encoded := mustJSON(readJSON(t, filepath.Join(directory, "settings.json")))
	if strings.Count(encoded, `"statusMessage":"Lobbyte"`) != 4 {
		t.Fatalf("concurrent setup handlers: %s", encoded)
	}
}

func TestSetupRepairsPrivateLockAndOwnershipModes(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	directory := filepath.Join(home, ".codex")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "hooks.json")
	manager := Manager{Home: home, Executable: "/tmp/lobbyte", RuntimeDir: filepath.Join(home, "runtime"), AgentAvailable: testAgentAvailable}
	if result, _ := manager.Setup(Codex); result.Status != "Configured" {
		t.Fatalf("initial setup result: %#v", result)
	}
	lockPath, ownershipPath := path+".lobbyte.lock", path+".lobbyte-owned.json"
	if err := os.Chmod(lockPath, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ownershipPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if result, _ := manager.Setup(Codex); result.Status != "Configured" {
		t.Fatalf("repair setup result: %#v", result)
	}
	for _, file := range []string{lockPath, ownershipPath} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%v err=%v", filepath.Base(file), info.Mode().Perm(), err)
		}
	}
}

func TestOwnershipWriteFailureRollsBackConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	directory := filepath.Join(home, ".codex")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "hooks.json")
	original := []byte("{}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".lobbyte-owned.json", 0o700); err != nil {
		t.Fatal(err)
	}
	result, present := (Manager{Home: home, Executable: "/tmp/lobbyte", RuntimeDir: filepath.Join(home, "runtime"), AgentAvailable: testAgentAvailable}).Setup(Codex)
	if !present || result.Status != "Disabled" {
		t.Fatalf("setup result=%#v present=%v", result, present)
	}
	if after := mustRead(t, path); string(after) != string(original) {
		t.Fatalf("config was not rolled back: %s", after)
	}
}

func writeJSON(t *testing.T, path string, object any) {
	t.Helper()
	data, _ := json.Marshal(object)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data := mustRead(t, path)
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func testAgentAvailable(Agent) bool { return true }
