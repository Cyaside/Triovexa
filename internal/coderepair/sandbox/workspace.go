package sandbox

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/security"
)

type Limits struct {
	MaxFileBytes   int64
	MaxTotalBytes  int64
	MaxListedFiles int
	MaxSearchHits  int
}

type SearchHit struct {
	Path string
	Line int
	Text string
}

// Workspace exposes only typed, bounded read operations inside a checked-out
// repository. It does not provide a command or arbitrary write operation.
type Workspace struct {
	root     *os.Root
	rootPath string
	binding  coderepair.RepositoryBinding
	limits   Limits
	mu       sync.Mutex
	read     int64
}

func Open(rootPath string, binding coderepair.RepositoryBinding, limits Limits) (*Workspace, error) {
	if err := binding.Validate(); err != nil {
		return nil, fmt.Errorf("invalid workspace binding: %w", err)
	}
	if limits.MaxFileBytes <= 0 || limits.MaxTotalBytes < limits.MaxFileBytes || limits.MaxListedFiles <= 0 || limits.MaxSearchHits <= 0 {
		return nil, errors.New("workspace requires positive, consistent limits")
	}
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("workspace root must be a real directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	return &Workspace{root: root, rootPath: rootPath, binding: binding, limits: limits}, nil
}

func (w *Workspace) Close() error { return w.root.Close() }

func (w *Workspace) RootPath() string { return w.rootPath }

func (w *Workspace) checkedPath(name string) error {
	if !w.binding.AllowsPath(name) {
		return errors.New("path is outside the repository binding or protected")
	}
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = path.Join(current, part)
		info, err := w.root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink in repository path is not allowed")
		}
	}
	return nil
}

func (w *Workspace) ReadFile(name string) ([]byte, error) {
	return w.readFile(name, true)
}

// SourceIndex owns unique-byte accounting for indexed reads. The same bounded
// file reader is reused without charging each cache miss as a new source file.
func (w *Workspace) readIndexedFile(name string) ([]byte, error) {
	return w.readFile(name, false)
}

func (w *Workspace) readFile(name string, charge bool) ([]byte, error) {
	if err := w.checkedPath(name); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if charge && w.read >= w.limits.MaxTotalBytes {
		return nil, errors.New("workspace read budget exhausted")
	}
	file, err := w.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > w.limits.MaxFileBytes || (charge && info.Size() > w.limits.MaxTotalBytes-w.read) {
		return nil, errors.New("file is not regular or exceeds workspace read budget")
	}
	data, err := io.ReadAll(io.LimitReader(file, w.limits.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > w.limits.MaxFileBytes || (charge && int64(len(data)) > w.limits.MaxTotalBytes-w.read) || strings.IndexByte(string(data), 0) >= 0 {
		return nil, errors.New("file is binary or exceeds workspace read budget")
	}
	if security.Redact(string(data)) != string(data) {
		return nil, errors.New("file contains a credential-like value")
	}
	if charge {
		w.read += int64(len(data))
	}
	return data, nil
}

func (w *Workspace) ListFiles(prefix string) ([]string, error) {
	if err := w.checkedPath(prefix); err != nil {
		return nil, err
	}
	var names []string
	err := fs.WalkDir(w.root.FS(), prefix, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !w.binding.AllowsPath(name) {
			return errors.New("protected path encountered in allowed scope")
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.New("symlink in repository scope is not allowed")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("non-regular file in repository scope")
		}
		names = append(names, name)
		if len(names) > w.limits.MaxListedFiles {
			return errors.New("workspace file listing limit exceeded")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

func (w *Workspace) SearchText(prefix, needle string) ([]SearchHit, error) {
	if needle == "" || len(needle) > 256 || strings.IndexByte(needle, 0) >= 0 {
		return nil, errors.New("search requires a bounded literal query")
	}
	names, err := w.ListFiles(prefix)
	if err != nil {
		return nil, err
	}
	var hits []SearchHit
	for _, name := range names {
		data, err := w.ReadFile(name)
		if err != nil {
			return nil, err
		}
		for index, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, needle) {
				if len(hits) == w.limits.MaxSearchHits {
					return nil, errors.New("workspace search result limit exceeded")
				}
				if len(line) > 512 {
					line = line[:512]
				}
				hits = append(hits, SearchHit{Path: name, Line: index + 1, Text: line})
			}
		}
	}
	return hits, nil
}
