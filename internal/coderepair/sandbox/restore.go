package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

const privateCheckoutMarker = ".git/triovexa-private-checkout"

// Only the private checkout factory creates this ownership marker. Repository
// paths and model tools cannot read or write .git metadata.
func markPrivateCheckout(root, baseSHA string) error {
	return os.WriteFile(filepath.Join(root, filepath.FromSlash(privateCheckoutMarker)), []byte(root+"\n"+baseSHA+"\n"), 0600)
}

// RestoreCandidate restores existing source files from the approved Git blobs.
// It cannot reset a developer checkout, remove files, restore the index, follow
// Git filters, or accept changes outside the previously validated candidate.
func (w *Workspace) RestoreCandidate(ctx context.Context, approvedSHA string, report PatchReport) error {
	if _, bounded := ctx.Deadline(); !bounded || !coderepair.ValidGitRevision(approvedSHA) {
		return errors.New("candidate restore requires a bounded approved revision")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if info, err := w.root.Lstat(".git"); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("candidate restore requires owned private Git metadata")
	}
	if info, err := w.root.Lstat(privateCheckoutMarker); err != nil || !info.Mode().IsRegular() {
		return errors.New("candidate restore requires an owned regular marker")
	}
	marker, err := w.root.Open(privateCheckoutMarker)
	if err != nil {
		return errors.New("candidate restore requires private checkout ownership")
	}
	info, statErr := marker.Stat()
	owned, readErr := io.ReadAll(io.LimitReader(marker, 4097))
	_ = marker.Close()
	if statErr != nil || !info.Mode().IsRegular() || readErr != nil || len(owned) > 4096 || string(owned) != w.rootPath+"\n"+approvedSHA+"\n" {
		return errors.New("candidate restore ownership does not match the approved checkout")
	}
	for _, query := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "HEAD"}, approvedSHA},
		{[]string{"rev-parse", "--abbrev-ref", "HEAD"}, "HEAD"},
	} {
		value, err := git(ctx, append([]string{"-C", w.rootPath}, query.args...)...)
		if err != nil || strings.TrimSpace(value) != query.want {
			return errors.New("candidate restore requires the detached approved base")
		}
	}
	top, err := git(ctx, "-C", w.rootPath, "rev-parse", "--show-toplevel")
	topInfo, topErr := os.Stat(filepath.Clean(filepath.FromSlash(strings.TrimSpace(top))))
	rootInfo, rootErr := w.root.Stat(".")
	if err != nil || topErr != nil || rootErr != nil || !os.SameFile(topInfo, rootInfo) {
		return errors.New("candidate restore requires an independent private repository")
	}
	if len(report.Files) > 5 {
		return errors.New("candidate restore exceeds the validated file limit")
	}
	allowed := make(map[string]bool, len(report.Files))
	for _, name := range report.Files {
		if allowed[name] || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || w.checkedPath(name) != nil {
			return errors.New("candidate restore path is not validated existing source")
		}
		info, err := w.root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("candidate restore source is not a regular file")
		}
		allowed[name] = true
	}
	status, err := git(ctx, "-C", w.rootPath, "status", "--porcelain", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return err
	}
	var changed []string
	if status != "" {
		for _, record := range strings.Split(strings.TrimSuffix(status, "\x00"), "\x00") {
			if !strings.HasPrefix(record, " M ") || !allowed[strings.TrimPrefix(record, " M ")] {
				return errors.New("candidate restore rejects new, staged, deleted or unexpected files")
			}
			changed = append(changed, strings.TrimPrefix(record, " M "))
		}
	}
	summary, err := git(ctx, "-C", w.rootPath, "diff", "--summary")
	if err != nil || strings.TrimSpace(summary) != "" {
		return errors.New("candidate restore rejects changes to file identity or mode")
	}
	// Preflight every original blob before writing even one file. Raw blobs do
	// not execute repository-controlled smudge filters or hooks.
	originals := make(map[string][]byte, len(changed))
	blobLimit := int(w.limits.MaxFileBytes)
	if blobLimit > 32*1024 {
		blobLimit = 32 * 1024
	}
	for _, name := range changed {
		blob, err := gitInputBounded(ctx, nil, blobLimit, "-C", w.rootPath, "show", approvedSHA+":"+name)
		if err != nil || !utf8.ValidString(blob) || strings.IndexByte(blob, 0) >= 0 {
			return errors.New("candidate restore cannot read the approved source blob")
		}
		originals[name] = []byte(blob)
	}
	for _, name := range changed {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.checkedPath(name); err != nil {
			return err
		}
		file, err := w.root.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		opened, openErr := file.Stat()
		current, currentErr := w.root.Lstat(name)
		if openErr != nil || currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
			_ = file.Close()
			return errors.New("candidate restore source identity changed")
		}
		truncateErr := file.Truncate(0)
		if truncateErr == nil {
			_, truncateErr = file.Write(originals[name])
		}
		closeErr := file.Close()
		if truncateErr != nil || closeErr != nil {
			return errors.New("candidate restore failed while restoring source")
		}
	}
	status, err = git(ctx, "-C", w.rootPath, "status", "--porcelain", "--untracked-files=all", "--ignored=matching")
	if err != nil || status != "" {
		return errors.New("candidate restore did not produce a clean approved base")
	}
	return nil
}
