package identity

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadOrCreatePersistsOneIdentity(t *testing.T) {
	t.Parallel()

	store, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identity changed: %#v != %#v", first, second)
	}
	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %o", info.Mode().Perm())
	}
}

func TestConcurrentLoadOrCreateConverges(t *testing.T) {
	t.Parallel()

	store, _ := NewFile(t.TempDir())
	const workers = 8
	results := make(chan Identity, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			identity, err := store.LoadOrCreate()
			results <- identity
			errorsChannel <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("LoadOrCreate error: %v", err)
		}
	}
	persisted, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	for result := range results {
		if result != persisted {
			t.Fatalf("returned identity differs from persisted identity: %#v != %#v", result, persisted)
		}
	}
}

func TestCorruptIdentityRequiresExplicitReplacement(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	store, _ := NewFile(directory)
	if err := os.WriteFile(store.path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOrCreate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("LoadOrCreate error = %v, want ErrCorrupt", err)
	}
	replacement, err := store.BackupAndCreateNew()
	if err != nil {
		t.Fatal(err)
	}
	if replacement.DeviceID == "" {
		t.Fatal("replacement identity is empty")
	}
	if _, err := os.Stat(filepath.Join(directory, "identity.json.invalid")); err != nil {
		t.Fatalf("identity backup missing: %v", err)
	}
}

func TestRecoveryCannotReplaceAValidIdentity(t *testing.T) {
	t.Parallel()

	store, _ := NewFile(t.TempDir())
	created, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BackupAndCreateNew(); err == nil {
		t.Fatal("BackupAndCreateNew replaced a valid identity")
	}
	loaded, err := store.LoadOrCreate()
	if err != nil || loaded != created {
		t.Fatalf("valid identity changed: %#v, err = %v", loaded, err)
	}
}

func TestExistingIdentityPermissionsAreRepaired(t *testing.T) {
	t.Parallel()

	store, _ := NewFile(t.TempDir())
	created, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.path, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadOrCreate()
	if err != nil || loaded != created {
		t.Fatalf("loaded = %#v, err = %v", loaded, err)
	}
	info, _ := os.Stat(store.path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %o", info.Mode().Perm())
	}
}

func TestExistingProfileDirectoryPermissionsAreRepaired(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	store, _ := NewFile(directory)
	if _, err := store.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("profile directory mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestExistingIdentityLockPermissionsAreRepaired(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	store, _ := NewFile(directory)
	lockPath := store.path + ".lock"
	if err := os.WriteFile(lockPath, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(lockPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity lock mode=%v err=%v", info.Mode().Perm(), err)
	}
}
