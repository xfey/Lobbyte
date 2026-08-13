package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRulesAcceptanceIsVersionedAndCorruptionRequiresAdmission(t *testing.T) {
	t.Parallel()

	profile := t.TempDir()
	if RulesAccepted(profile, "rules-1") {
		t.Fatal("missing settings were accepted")
	}
	if err := AcceptRules(profile, "rules-1"); err != nil {
		t.Fatal(err)
	}
	if !RulesAccepted(profile, "rules-1") || RulesAccepted(profile, "rules-2") {
		t.Fatal("rules acceptance was not version-specific")
	}
	path := filepath.Join(profile, "settings.json")
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if RulesAccepted(profile, "rules-1") {
		t.Fatal("corrupt settings were accepted")
	}
	if err := AcceptRules(profile, "rules-1"); err != nil || !RulesAccepted(profile, "rules-1") {
		t.Fatalf("settings recovery failed: %v", err)
	}
}
