package identity

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const settingsVersion = 1

type settings struct {
	Version              int    `json:"version"`
	AcceptedRulesVersion string `json:"accepted_rules_version,omitempty"`
}

// RulesAccepted reports whether the current local rules version was accepted.
// Missing or invalid settings safely require admission again.
func RulesAccepted(profileDir, rulesVersion string) bool {
	if !filepath.IsAbs(profileDir) || rulesVersion == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(profileDir, "settings.json"))
	if err != nil {
		return false
	}
	var value settings
	return json.Unmarshal(data, &value) == nil && value.Version == settingsVersion && value.AcceptedRulesVersion == rulesVersion
}

// AcceptRules atomically records only the accepted published rules version.
func AcceptRules(profileDir, rulesVersion string) error {
	if !filepath.IsAbs(profileDir) || rulesVersion == "" {
		return errors.New("absolute profile directory and rules version are required")
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(profileDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings{Version: settingsVersion, AcceptedRulesVersion: rulesVersion}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(profileDir, "settings.json"), append(data, '\n'), fs.FileMode(0o600))
}
