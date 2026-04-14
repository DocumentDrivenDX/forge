package safefs

import (
	"fmt"
	"os"
	"path/filepath"
)

// ReadFile reads a file from a user-selected path.
// #nosec G304 -- callers intentionally operate on user-selected paths.
func ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

// WriteFile writes a file with an explicit mode.
// #nosec G306 -- callers intentionally write to user-selected paths.
func WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// MkdirAll creates a directory tree with an explicit mode.
// #nosec G301 -- callers intentionally manage user-selected paths.
func MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// Chmod changes file permissions.
// #nosec G302 -- update downloads must be made executable.
func Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}

// WriteFileAtomic writes data to name atomically by writing to a temporary file
// in the same directory and then renaming it into place. This prevents readers
// from observing a partially-written file if the process is interrupted.
// #nosec G304 -- callers intentionally operate on user-selected paths.
func WriteFileAtomic(name string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(name)
	tmp, err := os.CreateTemp(dir, ".tmp-safefs-*")
	if err != nil {
		return fmt.Errorf("safefs: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup on failure.
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("safefs: write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("safefs: chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("safefs: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, name); err != nil {
		return fmt.Errorf("safefs: rename temp file: %w", err)
	}
	ok = true
	return nil
}

// Remove deletes a file if it exists.
// #nosec G104 -- cleanup errors are intentionally ignored by callers.
func Remove(name string) error {
	return os.Remove(name)
}

// Create opens a file for writing with a private mode.
// #nosec G304 -- callers intentionally create files under user-selected directories.
func Create(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
}
