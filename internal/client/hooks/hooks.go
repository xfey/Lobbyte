// Package hooks owns Codex and Claude Code hook configuration.
package hooks

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var hookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}

const hookCommandDescription = "# Lobbyte: updates local TUI status only; hook input is discarded."

// Agent identifies one supported hook configuration.
type Agent string

const (
	Codex      Agent = "codex"
	ClaudeCode Agent = "claude-code"
)

// Result is the compact user-facing setup status.
type Result struct {
	Agent  Agent
	Status string
	Reason string
}

// Manager updates both user-level agent configs transactionally per agent.
type Manager struct {
	Home           string
	Executable     string
	RuntimeDir     string
	AgentAvailable func(Agent) bool
}

// SetupAll attempts both agents and silently omits agents with no config directory.
func (manager Manager) SetupAll() []Result {
	results := make([]Result, 0, 2)
	for _, agent := range []Agent{Codex, ClaudeCode} {
		result, present := manager.Setup(agent)
		if present {
			results = append(results, result)
		}
	}
	return results
}

// Setup adds or repairs exactly Lobbyte's four lifecycle handlers.
func (manager Manager) Setup(agent Agent) (Result, bool) {
	path, directory, err := manager.configPath(agent)
	if err != nil {
		return Result{Agent: agent, Status: "Disabled", Reason: err.Error()}, true
	}
	if _, err := os.Stat(directory); errors.Is(err, fs.ErrNotExist) {
		return Result{}, false
	}
	if !manager.agentAvailable(agent) {
		return Result{}, false
	}
	if agent == ClaudeCode {
		disabled, reason := claudePolicy(path)
		if disabled {
			return Result{Agent: agent, Status: "Disabled", Reason: reason}, true
		}
	}
	if agent == Codex {
		disabled, reason := codexPolicy(filepath.Join(manager.Home, ".codex", "config.toml"))
		if disabled {
			return Result{Agent: agent, Status: "Disabled", Reason: reason}, true
		}
	}
	if err := manager.update(path, agent, false); err != nil {
		return Result{Agent: agent, Status: "Disabled", Reason: "could not update config"}, true
	}
	reason := ""
	if agent == Codex {
		reason = "review required"
	}
	return Result{Agent: agent, Status: "Configured", Reason: reason}, true
}

func (manager Manager) agentAvailable(agent Agent) bool {
	if manager.AgentAvailable != nil {
		return manager.AgentAvailable(agent)
	}
	command := "codex"
	if agent == ClaudeCode {
		command = "claude"
	}
	_, err := exec.LookPath(command)
	return err == nil
}

// RemoveAll removes only handlers whose exact structure is owned by Lobbyte.
func (manager Manager) RemoveAll() error {
	var failures []error
	for _, agent := range []Agent{Codex, ClaudeCode} {
		path, _, err := manager.configPath(agent)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			if _, ownerErr := os.Stat(path + ".lobbyte-owned.json"); errors.Is(ownerErr, fs.ErrNotExist) {
				continue
			}
		}
		if err := manager.update(path, agent, true); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (manager Manager) configPath(agent Agent) (string, string, error) {
	if !filepath.IsAbs(manager.Home) || !filepath.IsAbs(manager.Executable) || !filepath.IsAbs(manager.RuntimeDir) {
		return "", "", errors.New("home, executable, and runtime paths must be absolute")
	}
	switch agent {
	case Codex:
		directory := filepath.Join(manager.Home, ".codex")
		return filepath.Join(directory, "hooks.json"), directory, nil
	case ClaudeCode:
		directory := filepath.Join(manager.Home, ".claude")
		return filepath.Join(directory, "settings.json"), directory, nil
	default:
		return "", "", errors.New("unsupported agent")
	}
}

func (manager Manager) update(path string, agent Agent, remove bool) error {
	lock, err := os.OpenFile(path+".lobbyte.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	originalBytes, originalExists, err := readConfig(path)
	if err != nil {
		return err
	}
	original, mode, err := decodeObject(path, originalBytes, originalExists)
	if err != nil {
		return err
	}
	if err := validateHookStructure(original); err != nil {
		return err
	}
	owned, err := readOwnership(path + ".lobbyte-owned.json")
	if err != nil {
		return err
	}
	ownershipPath := path + ".lobbyte-owned.json"
	if _, err := os.Stat(ownershipPath); err == nil {
		if err := os.Chmod(ownershipPath, 0o600); err != nil {
			return err
		}
	}
	updated := cloneObject(original)
	newOwned := make(map[string]map[string]any)
	if remove {
		removeOwned(updated, manager.Executable, manager.RuntimeDir, agent, owned)
	} else {
		for _, event := range hookEvents {
			expected := owned[event]
			if expected == nil {
				expected = handler(manager.Executable, manager.RuntimeDir, agent, event)
			}
			removeOwnedEvent(updated, expected, event)
			current := handler(manager.Executable, manager.RuntimeDir, agent, event)
			if !objectsEqual(expected, current) {
				removeOwnedEvent(updated, current, event)
			}
			if err := addOwnedEvent(updated, manager.Executable, manager.RuntimeDir, agent, event, eventMatcher(agent, event)); err != nil {
				return err
			}
			newOwned[event] = current
		}
	}
	ownershipChanged := !objectsEqual(owned, newOwned)
	if objectsEqual(original, updated) && !ownershipChanged {
		return nil
	}
	currentBytes, currentExists, err := readConfig(path)
	if err != nil {
		return err
	}
	if currentExists != originalExists || string(currentBytes) != string(originalBytes) {
		return errors.New("config changed while it was being updated")
	}
	if originalExists && !objectsEqual(original, updated) {
		if err := backup(path, originalBytes, mode); err != nil {
			return err
		}
	}
	if remove {
		configChanged := !objectsEqual(original, updated)
		if configChanged {
			if err := writeObject(path, updated, mode); err != nil {
				return err
			}
		}
		if err := os.Remove(ownershipPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if configChanged {
				if rollbackErr := writeRaw(path, originalBytes, mode, originalExists); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
			}
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		return nil
	}
	if !objectsEqual(original, updated) {
		if err := writeObject(path, updated, mode); err != nil {
			return err
		}
	}
	if err := writeOwnership(ownershipPath, newOwned); err != nil {
		if rollbackErr := writeRaw(path, originalBytes, mode, originalExists); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

func handler(executable, runtimeDir string, agent Agent, event string) map[string]any {
	command := shellCommand(executable, runtimeDir, agent, event)
	if agent == ClaudeCode {
		return map[string]any{
			"type": "command", "command": "exec /bin/sh -c " + shellQuote(command),
			"timeout": float64(1), "statusMessage": "Lobbyte",
		}
	}
	return map[string]any{
		"type": "command", "command": command, "timeout": float64(1),
	}
}

func eventMatcher(agent Agent, event string) string {
	if event != "SessionStart" {
		return ""
	}
	if agent == ClaudeCode {
		return "^(startup|resume|clear|fork)$"
	}
	return "^(startup|resume|clear)$"
}

func shellCommand(executable, runtimeDir string, agent Agent, event string) string {
	quotedExecutable := shellQuote(executable)
	return fmt.Sprintf("%s\nif [ -x %s ]; then exec %s hook %s %s %s; else cat >/dev/null; fi",
		hookCommandDescription,
		quotedExecutable, quotedExecutable, agent, event, shellQuote(runtimeDir))
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func addOwnedEvent(object map[string]any, executable, runtimeDir string, agent Agent, event, matcher string) error {
	hooks, err := hooksObject(object)
	if err != nil {
		return err
	}
	groups := []any(nil)
	if raw, exists := hooks[event]; exists {
		var ok bool
		groups, ok = raw.([]any)
		if !ok {
			return fmt.Errorf("incompatible %s hook structure", event)
		}
	}
	group := map[string]any{"hooks": []any{handler(executable, runtimeDir, agent, event)}}
	if matcher != "" {
		group["matcher"] = matcher
	}
	hooks[event] = append(groups, group)
	return nil
}

func hooksObject(object map[string]any) (map[string]any, error) {
	if object == nil {
		return nil, errors.New("hook config root must be an object")
	}
	raw, exists := object["hooks"]
	if !exists {
		created := map[string]any{}
		object["hooks"] = created
		return created, nil
	}
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("incompatible hooks structure")
	}
	return hooks, nil
}

func validateHookStructure(object map[string]any) error {
	if object == nil {
		return errors.New("hook config root must be an object")
	}
	rawHooks, exists := object["hooks"]
	if !exists {
		return nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return errors.New("incompatible hooks structure")
	}
	for _, event := range hookEvents {
		rawGroups, exists := hooks[event]
		if !exists {
			continue
		}
		groups, ok := rawGroups.([]any)
		if !ok {
			return fmt.Errorf("incompatible %s hook structure", event)
		}
		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				return fmt.Errorf("incompatible %s hook group", event)
			}
			rawHandlers, exists := group["hooks"]
			if !exists {
				return fmt.Errorf("incompatible %s hook group", event)
			}
			handlers, ok := rawHandlers.([]any)
			if !ok {
				return fmt.Errorf("incompatible %s handlers", event)
			}
			for _, rawHandler := range handlers {
				if _, ok := rawHandler.(map[string]any); !ok {
					return fmt.Errorf("incompatible %s handler", event)
				}
			}
		}
	}
	return nil
}

func removeOwned(object map[string]any, executable, runtimeDir string, agent Agent, owned map[string]map[string]any) {
	for _, event := range hookEvents {
		expected := owned[event]
		if expected == nil {
			expected = handler(executable, runtimeDir, agent, event)
		}
		removeOwnedEvent(object, expected, event)
	}
}

func removeOwnedEvent(object map[string]any, expected map[string]any, event string) {
	hooks, ok := object["hooks"].(map[string]any)
	if !ok {
		return
	}
	groups := ensureSlice(hooks[event])
	keptGroups := make([]any, 0, len(groups))
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			keptGroups = append(keptGroups, rawGroup)
			continue
		}
		handlers := ensureSlice(group["hooks"])
		keptHandlers := make([]any, 0, len(handlers))
		for _, rawHandler := range handlers {
			candidate, ok := rawHandler.(map[string]any)
			if !ok || !objectsEqual(candidate, expected) {
				keptHandlers = append(keptHandlers, rawHandler)
			}
		}
		if len(keptHandlers) > 0 {
			group["hooks"] = keptHandlers
			keptGroups = append(keptGroups, group)
		} else if len(group) > 2 || (len(group) == 2 && group["matcher"] == nil) {
			group["hooks"] = keptHandlers
			keptGroups = append(keptGroups, group)
		}
	}
	if len(keptGroups) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = keptGroups
	}
	if len(hooks) == 0 {
		delete(object, "hooks")
	}
}

func readConfig(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func decodeObject(path string, data []byte, exists bool) (map[string]any, fs.FileMode, error) {
	if !exists {
		return map[string]any{}, 0o600, nil
	}
	if len(data) == 0 {
		return map[string]any{}, fileMode(path), nil
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, 0, fmt.Errorf("invalid JSON config: %w", err)
	}
	if object == nil {
		return nil, 0, errors.New("JSON config root must be an object")
	}
	return object, fileMode(path), nil
}

func readObject(path string) (map[string]any, fs.FileMode, error) {
	data, exists, err := readConfig(path)
	if err != nil {
		return nil, 0, err
	}
	return decodeObject(path, data, exists)
}

func fileMode(path string) fs.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return 0o600
	}
	return info.Mode().Perm()
}

func writeObject(path string, object map[string]any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".lobbyte-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(mode); err != nil {
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
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readOwnership(path string) (map[string]map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return make(map[string]map[string]any), nil
	}
	if err != nil {
		return nil, err
	}
	var owned map[string]map[string]any
	if err := json.Unmarshal(data, &owned); err != nil {
		return nil, fmt.Errorf("invalid Lobbyte hook ownership: %w", err)
	}
	return owned, nil
}

func writeOwnership(path string, owned map[string]map[string]any) error {
	object := make(map[string]any, len(owned))
	for event, value := range owned {
		object[event] = value
	}
	return writeObject(path, object, 0o600)
}

func writeRaw(path string, data []byte, mode fs.FileMode, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".lobbyte-rollback-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func backup(path string, data []byte, mode fs.FileMode) error {
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	backupPath := path + ".lobbyte." + stamp + ".bak"
	if err := os.WriteFile(backupPath, data, mode); err != nil {
		return err
	}
	backups, _ := filepath.Glob(path + ".lobbyte.*.bak")
	sort.Strings(backups)
	for len(backups) > 3 {
		if err := os.Remove(backups[0]); err != nil {
			return err
		}
		backups = backups[1:]
	}
	return nil
}

func claudePolicy(path string) (bool, string) {
	object, _, err := readObject(path)
	if err != nil {
		return true, "could not read policy"
	}
	if disabled, _ := object["disableAllHooks"].(bool); disabled {
		return true, "hooks disabled"
	}
	if managed, _ := object["allowManagedHooksOnly"].(bool); managed {
		return true, "managed hooks only"
	}
	return false, ""
}

func codexPolicy(path string) (bool, string) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, ""
	}
	if err != nil {
		return true, "could not read policy"
	}
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if section == "features" && (key == "hooks" || key == "codex_hooks") && value == "false" {
			return true, "hooks disabled"
		}
		if section == "" && key == "allow_managed_hooks_only" && value == "true" {
			return true, "managed hooks only"
		}
	}
	if scanner.Err() != nil {
		return true, "could not read policy"
	}
	return false, ""
}

func stripTOMLComment(line string) string {
	var quote rune
	escaped := false
	for index, character := range line {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			continue
		}
		if character == '\'' || character == '"' {
			if quote == 0 {
				quote = character
			} else if quote == character {
				quote = 0
			}
			continue
		}
		if character == '#' && quote == 0 {
			return line[:index]
		}
	}
	return line
}

func ensureSlice(value any) []any {
	if existing, ok := value.([]any); ok {
		return existing
	}
	return nil
}

func cloneObject(object map[string]any) map[string]any {
	data, _ := json.Marshal(object)
	var clone map[string]any
	_ = json.Unmarshal(data, &clone)
	return clone
}

func objectsEqual(first, second any) bool {
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	return string(left) == string(right)
}
