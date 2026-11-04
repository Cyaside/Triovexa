package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// diffContents uses a disposable index. Neither the real index nor working
// files are modified, and no repository-defined diff driver is executed.
func (w *Workspace) diffContents(ctx context.Context, revision string, contents map[string][]byte, limit int) ([]byte, error) {
	head, err := git(ctx, "-C", w.rootPath, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != revision {
		return nil, errors.New("edit base revision changed")
	}
	status, err := git(ctx, "-C", w.rootPath, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return nil, errors.New("edit generation requires a clean checkout")
	}
	directory, err := os.MkdirTemp("", "triovexa-edit-index-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(directory, "index")}
	run := func(input []byte, max int, args ...string) (string, error) {
		fixed := []string{"--literal-pathspecs", "-C", w.rootPath, "-c", "core.autocrlf=false", "-c", "core.quotePath=true", "-c", "diff.algorithm=myers", "-c", "diff.indentHeuristic=false"}
		return gitInputEnvironment(ctx, input, max, env, append(fixed, args...)...)
	}
	if _, err := run(nil, 4096, "read-tree", revision); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(contents))
	for name := range contents {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		stage, err := run(nil, 4096, "ls-files", "--stage", "-z", "--", name)
		parts := strings.SplitN(stage, "\t", 2)
		if err != nil || len(parts) != 2 || parts[1] != name+"\x00" {
			return nil, errors.New("edit target must be a tracked regular file")
		}
		fields := strings.Fields(parts[0])
		if len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[2] != "0" {
			return nil, errors.New("edit target has an unsupported mode")
		}
		blob, err := run(contents[name], 4096, "hash-object", "-w", "--stdin")
		if err != nil {
			return nil, err
		}
		if _, err := run(nil, 4096, "update-index", "--cacheinfo", fields[0], strings.TrimSpace(blob), name); err != nil {
			return nil, err
		}
	}
	patch, err := run(nil, limit, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-color", "--full-index", "--src-prefix=a/", "--dst-prefix=b/", "--unified=3", revision, "--")
	if err != nil {
		return nil, err
	}
	if patch == "" {
		return nil, errors.New("proposal makes no source change")
	}
	return []byte(patch), nil
}
