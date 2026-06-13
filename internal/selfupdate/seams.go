// Package selfupdate implements the binary self-update engine: semver decision,
// asset download, SHA256 verification, and atomic swap.
//
// Filesystem and process seams are defined locally because this package's swap
// operation (WriteTemp + Rename + Chmod + Remove + Stat + ProbeWritable) is
// specific to the atomic-rename pattern and does not belong in the shared
// dockerx.FS interface (which covers VM paths for docker/detect operations).
// Keeping them local avoids coupling unrelated packages and makes the seam
// surface minimal and purpose-built.
package selfupdate

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FileOps abstracts the filesystem operations required for the atomic binary
// swap. The real implementation delegates to os.* functions; fakes record calls
// for unit tests without touching the real filesystem.
type FileOps interface {
	// Rename atomically renames (moves) old to new. On Linux, os.Rename maps
	// to rename(2), which is atomic within a single filesystem.
	Rename(old, new string) error

	// WriteTemp streams r into a new temporary file inside dir and returns its
	// path. The caller is responsible for removing the file on any failure path.
	WriteTemp(dir string, r io.Reader) (path string, err error)

	// Remove deletes the named file.
	Remove(path string) error

	// Chmod changes the mode bits of the named file.
	Chmod(path string, mode fs.FileMode) error

	// Stat returns file info for path. Used to confirm binary existence.
	Stat(path string) (fs.FileInfo, error)

	// ProbeWritable checks whether the current process can write to path. It
	// does NOT create or modify the file. Returns nil when writable.
	ProbeWritable(path string) error
}

// ExecutableResolver resolves the path of the currently running binary.
// Abstracting os.Executable lets tests inject a controlled path.
type ExecutableResolver interface {
	// Executable returns the path to the currently running binary, following
	// the same semantics as os.Executable (may be a symlink).
	Executable() (string, error)
}

// ─── Real implementations ────────────────────────────────────────────────────

// realFileOps is the production FileOps backed by os.* calls.
type realFileOps struct{}

func (realFileOps) Rename(old, new string) error {
	return os.Rename(old, new)
}

func (realFileOps) WriteTemp(dir string, r io.Reader) (string, error) {
	f, err := os.CreateTemp(dir, ".crenein-agent.new-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err = io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (realFileOps) Remove(path string) error {
	return os.Remove(path)
}

func (realFileOps) Chmod(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode)
}

func (realFileOps) Stat(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

// ProbeWritable attempts to open path for append without truncating. If the
// path does not exist it tries to create a probe file in the same directory
// and immediately removes it.
func (realFileOps) ProbeWritable(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err == nil {
		_ = f.Close()
		return nil
	}
	// Path may not exist yet (first install). Try a probe file in the dir.
	dir := filepath.Dir(path)
	probe, err2 := os.CreateTemp(dir, ".crenein-probe-*")
	if err2 != nil {
		return fmt.Errorf("cannot write to %s: %w", path, err2)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return nil
}

// realExecutableResolver is the production ExecutableResolver backed by
// os.Executable.
type realExecutableResolver struct{}

func (realExecutableResolver) Executable() (string, error) {
	return os.Executable()
}
