// Package identity owns the local device identity and client configuration.
package identity

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const identityVersion = 1

var ErrCorrupt = errors.New("identity file is invalid")

// Identity is the complete local persisted identity document.
type Identity struct {
	Version  int    `json:"version"`
	DeviceID string `json:"device_id"`
}

// File stores a device identity inside one client profile.
type File struct{ path string }

// NewFile returns the identity store for an absolute client profile directory.
func NewFile(profileDir string) (File, error) {
	if profileDir == "" || !filepath.IsAbs(profileDir) {
		return File{}, errors.New("identity profile directory must be absolute")
	}
	return File{path: filepath.Join(profileDir, "identity.json")}, nil
}

// LoadOrCreate returns the existing identity or atomically creates one if absent.
func (file File) LoadOrCreate() (Identity, error) {
	var identity Identity
	err := file.withLock(func() error {
		var loadErr error
		identity, loadErr = file.loadOrCreateLocked()
		return loadErr
	})
	return identity, err
}

func (file File) loadOrCreateLocked() (Identity, error) {
	data, err := os.ReadFile(file.path)
	if err == nil {
		if chmodErr := os.Chmod(file.path, 0o600); chmodErr != nil {
			return Identity{}, fmt.Errorf("protect identity: %w", chmodErr)
		}
		return decode(data)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Identity{}, fmt.Errorf("read identity: %w", err)
	}
	return file.create(false)
}

// BackupAndCreateNew preserves one corrupt identity and creates a new device identity.
func (file File) BackupAndCreateNew() (Identity, error) {
	var identity Identity
	err := file.withLock(func() error {
		var replaceErr error
		identity, replaceErr = file.backupAndCreateNewLocked()
		return replaceErr
	})
	return identity, err
}

func (file File) backupAndCreateNewLocked() (Identity, error) {
	data, err := os.ReadFile(file.path)
	if err != nil {
		return Identity{}, fmt.Errorf("read invalid identity: %w", err)
	}
	if _, decodeErr := decode(data); decodeErr == nil {
		return Identity{}, errors.New("identity is valid and must not be replaced by recovery")
	}
	backup := file.path + ".invalid"
	if err := writeAtomic(backup, data, 0o600); err != nil {
		return Identity{}, fmt.Errorf("backup invalid identity: %w", err)
	}
	return file.create(true)
}

func (file File) withLock(action func() error) error {
	if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
		return fmt.Errorf("create identity directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(file.path), 0o700); err != nil {
		return fmt.Errorf("protect identity directory: %w", err)
	}
	lock, err := os.OpenFile(file.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open identity lock: %w", err)
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return fmt.Errorf("protect identity lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock identity: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	return action()
}

func (file File) create(directoryExists bool) (Identity, error) {
	if !directoryExists {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			return Identity{}, fmt.Errorf("create identity directory: %w", err)
		}
	}
	if err := os.Chmod(filepath.Dir(file.path), 0o700); err != nil {
		return Identity{}, fmt.Errorf("protect identity directory: %w", err)
	}
	value, err := uuid.NewRandomFromReader(rand.Reader)
	if err != nil {
		return Identity{}, fmt.Errorf("generate device id: %w", err)
	}
	identity := Identity{Version: identityVersion, DeviceID: value.String()}
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return Identity{}, fmt.Errorf("encode identity: %w", err)
	}
	data = append(data, '\n')
	if err := writeAtomic(file.path, data, 0o600); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func decode(data []byte) (Identity, error) {
	var identity Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if identity.Version != identityVersion {
		return Identity{}, fmt.Errorf("%w: unsupported version %d", ErrCorrupt, identity.Version)
	}
	value, err := uuid.Parse(identity.DeviceID)
	if err != nil || value.Version() != 4 {
		return Identity{}, fmt.Errorf("%w: device_id is not UUID v4", ErrCorrupt)
	}
	return identity, nil
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".identity-*")
	if err != nil {
		return fmt.Errorf("create temporary identity: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary identity: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary identity: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary identity: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary identity: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace identity: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open identity directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync identity directory: %w", err)
	}
	return nil
}
