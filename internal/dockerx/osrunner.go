package dockerx

import (
	"context"
	"os"
	"os/exec"
)

// OSCommandRunner is the real implementation of CommandRunner. It shells out
// via exec.CommandContext and captures combined stdout+stderr.
type OSCommandRunner struct{}

// NewOSCommandRunner returns a CommandRunner backed by the real OS.
func NewOSCommandRunner() CommandRunner {
	return &OSCommandRunner{}
}

// Run executes name with args and returns combined output.
func (r *OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// LookPath reports whether the named binary exists in PATH.
func (r *OSCommandRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// OSFS is the real implementation of FS backed by the host filesystem.
type OSFS struct{}

// NewOSFS returns an FS backed by the real host filesystem.
func NewOSFS() FS {
	return &OSFS{}
}

// ReadFile reads the named file.
func (f *OSFS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

// WriteFile writes data to the named file.
func (f *OSFS) WriteFile(name string, data []byte, perm uint32) error {
	return os.WriteFile(name, data, os.FileMode(perm))
}

// MkdirAll creates path and all required parents.
func (f *OSFS) MkdirAll(path string, perm uint32) error {
	return os.MkdirAll(path, os.FileMode(perm))
}

// Stat returns minimal info about name.
func (f *OSFS) Stat(name string) (FileInfo, error) {
	info, err := os.Stat(name)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:  info.Name(),
		Mode:  uint32(info.Mode()),
		Size:  info.Size(),
		IsDir: info.IsDir(),
	}, nil
}

// Chmod changes the mode of name.
func (f *OSFS) Chmod(name string, perm uint32) error {
	return os.Chmod(name, os.FileMode(perm))
}

// Chown changes the uid/gid of name.
func (f *OSFS) Chown(name string, uid, gid int) error {
	return os.Chown(name, uid, gid)
}

// ReadDir returns the names of entries in path.
func (f *OSFS) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// RemoveAll removes path and any children it contains.
func (f *OSFS) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// AppendFile appends data to the named file (O_APPEND|O_CREATE|O_WRONLY).
// perm is used only when the file is created.
func (f *OSFS) AppendFile(name string, data []byte, perm uint32) error {
	fh, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, os.FileMode(perm))
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fh.Write(data)
	return err
}
