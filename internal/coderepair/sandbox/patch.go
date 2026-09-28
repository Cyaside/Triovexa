package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Cyaside/Triovexa/internal/security"
)

type PatchLimits struct {
	MaxPatchBytes   int
	MaxFiles        int
	MaxChangedLines int
}

type PatchReport struct {
	SHA256       string
	Files        []string
	AddedLines   int
	RemovedLines int
}

func (l PatchLimits) validate() error {
	if l.MaxPatchBytes <= 0 || l.MaxPatchBytes > 256*1024 || l.MaxFiles <= 0 || l.MaxFiles > 20 ||
		l.MaxChangedLines <= 0 || l.MaxChangedLines > 1000 {
		return errors.New("patch limits must be positive and within hard safety caps")
	}
	return nil
}

// ValidatePatch checks a model-proposed diff without changing the checkout.
// Only text modifications to existing regular files are permitted.
func (w *Workspace) ValidatePatch(ctx context.Context, patch []byte, limits PatchLimits) (PatchReport, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return PatchReport{}, errors.New("patch validation requires a deadline")
	}
	if err := limits.validate(); err != nil {
		return PatchReport{}, err
	}
	if len(patch) == 0 || len(patch) > limits.MaxPatchBytes || !utf8.Valid(patch) || strings.IndexByte(string(patch), 0) >= 0 {
		return PatchReport{}, errors.New("patch is empty, oversized, or not text")
	}
	if security.Redact(string(patch)) != string(patch) {
		return PatchReport{}, errors.New("patch contains a credential-like value")
	}
	if !strings.HasPrefix(string(patch), "diff --git ") || strings.Contains(string(patch), "GIT binary patch") ||
		strings.Contains(string(patch), "Binary files ") {
		return PatchReport{}, errors.New("patch must be a text Git diff")
	}
	status, err := git(ctx, "-C", w.rootPath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return PatchReport{}, err
	}
	if status != "" {
		return PatchReport{}, errors.New("checkout must be clean before proposing a patch")
	}
	summary, err := gitInput(ctx, patch, "-C", w.rootPath, "apply", "--summary", "-")
	if err != nil {
		return PatchReport{}, err
	}
	if strings.TrimSpace(summary) != "" {
		return PatchReport{}, errors.New("patch changes file identity or mode")
	}
	numstat, err := gitInput(ctx, patch, "-C", w.rootPath, "apply", "--numstat", "-z", "-")
	if err != nil {
		return PatchReport{}, err
	}
	if numstat == "" || !strings.HasSuffix(numstat, "\x00") {
		return PatchReport{}, errors.New("patch has no parseable changed files")
	}
	report := PatchReport{}
	seen := make(map[string]struct{})
	for _, record := range strings.Split(strings.TrimSuffix(numstat, "\x00"), "\x00") {
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 || parts[0] == "-" || parts[1] == "-" || parts[2] == "" {
			return PatchReport{}, errors.New("patch contains unsupported file change")
		}
		added, addErr := strconv.Atoi(parts[0])
		removed, removeErr := strconv.Atoi(parts[1])
		if addErr != nil || removeErr != nil || added < 0 || removed < 0 {
			return PatchReport{}, errors.New("patch has invalid line counts")
		}
		name := parts[2]
		if _, duplicate := seen[name]; duplicate {
			return PatchReport{}, errors.New("patch changes the same file twice")
		}
		if err := w.checkedPath(name); err != nil {
			return PatchReport{}, fmt.Errorf("patch path rejected: %w", err)
		}
		seen[name] = struct{}{}
		report.Files = append(report.Files, name)
		report.AddedLines += added
		report.RemovedLines += removed
		if len(report.Files) > limits.MaxFiles || report.AddedLines+report.RemovedLines > limits.MaxChangedLines {
			return PatchReport{}, errors.New("patch exceeds file or changed-line budget")
		}
	}
	if _, err := gitInput(ctx, patch, "-C", w.rootPath, "apply", "--check", "-"); err != nil {
		return PatchReport{}, err
	}
	digest := sha256.Sum256(patch)
	report.SHA256 = hex.EncodeToString(digest[:])
	return report, nil
}

// ApplyPatch applies only a diff that currently passes validation. The
// checkout is private and must be discarded if post-apply status is unexpected.
func (w *Workspace) ApplyPatch(ctx context.Context, patch []byte, limits PatchLimits) (PatchReport, error) {
	report, err := w.ValidatePatch(ctx, patch, limits)
	if err != nil {
		return PatchReport{}, err
	}
	if _, err := gitInput(ctx, patch, "-C", w.rootPath, "apply", "-"); err != nil {
		return PatchReport{}, err
	}
	status, err := git(ctx, "-C", w.rootPath, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return PatchReport{}, err
	}
	changed := make(map[string]struct{})
	for _, record := range strings.Split(strings.TrimSuffix(status, "\x00"), "\x00") {
		if !strings.HasPrefix(record, " M ") {
			return PatchReport{}, errors.New("patch produced an unexpected checkout state")
		}
		changed[strings.TrimPrefix(record, " M ")] = struct{}{}
	}
	if len(changed) != len(report.Files) {
		return PatchReport{}, errors.New("patch changed an unexpected number of files")
	}
	for _, name := range report.Files {
		if _, ok := changed[name]; !ok {
			return PatchReport{}, errors.New("patch changed a file outside the validated diff")
		}
	}
	return report, nil
}
