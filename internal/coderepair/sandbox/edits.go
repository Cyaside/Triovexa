package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Cyaside/Triovexa/internal/security"
)

const EditFormat = "text-edits-v1"

type TextEdit struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// Empty new_text is a deletion, whereas an omitted field is a contract error.
func (e *TextEdit) UnmarshalJSON(data []byte) error {
	var fields struct {
		OldText *string `json:"old_text"`
		NewText *string `json:"new_text"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&fields); err != nil {
		return errors.New("invalid edit fields")
	}
	if fields.OldText == nil || fields.NewText == nil {
		return errors.New("edit requires old_text and new_text")
	}
	e.OldText, e.NewText = *fields.OldText, *fields.NewText
	return nil
}

type FileEdits struct {
	Path           string     `json:"path"`
	ExpectedDigest string     `json:"expected_digest"`
	Edits          []TextEdit `json:"edits"`
}

type editSpan struct {
	start, end int
	text       string
}

// logicalText exposes LF while retaining the byte representation for writing.
func logicalText(data []byte) (string, string, error) {
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", "", errors.New("source must be UTF-8 text")
	}
	text := string(data)
	newline := "lf"
	if strings.Contains(text, "\r") {
		text = strings.ReplaceAll(text, "\r\n", "\n")
		if strings.Contains(text, "\r") || strings.Count(string(data), "\r\n") != strings.Count(text, "\n") {
			return "", "", errors.New("mixed or unsupported source line endings")
		}
		newline = "crlf"
	} else if !strings.Contains(text, "\n") {
		newline = "none"
	}
	return text, newline, nil
}

// BuildPatch resolves all replacements against already-read source at the
// approved revision. It never applies a candidate or runs repository commands.
func (s *SourceIndex) BuildPatch(ctx context.Context, files []FileEdits, limits PatchLimits) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("edit generation requires a deadline")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	if len(files) == 0 || len(files) > limits.MaxFiles {
		return nil, errors.New("edit file budget exceeded")
	}
	contents := make(map[string][]byte, len(files))
	totalEdits := 0
	for _, file := range files {
		if _, duplicate := contents[file.Path]; duplicate || !s.workspace.binding.CanPatchPath(file.Path) {
			return nil, errors.New("duplicate or protected edit path")
		}
		if len(file.Edits) == 0 || len(file.Edits) > 10 {
			return nil, errors.New("edit count is outside limits")
		}
		totalEdits += len(file.Edits)
		if totalEdits > 20 {
			return nil, errors.New("proposal edit budget exceeded")
		}
		source, err := s.source(file.Path)
		if err != nil {
			return nil, err
		}
		if source.digest != file.ExpectedDigest {
			return nil, errors.New("edit source digest is stale or invalid")
		}
		text, newline, err := logicalText(source.data)
		if err != nil {
			return nil, err
		}
		spans := make([]editSpan, 0, len(file.Edits))
		for _, edit := range file.Edits {
			if edit.OldText == "" || edit.OldText == edit.NewText || len(edit.OldText) > 16*1024 || len(edit.NewText) > 16*1024 ||
				!utf8.ValidString(edit.OldText) || !utf8.ValidString(edit.NewText) || strings.ContainsAny(edit.OldText+edit.NewText, "\r\x00") ||
				security.Redact(edit.NewText) != edit.NewText {
				return nil, errors.New("replacement is empty, unchanged, oversized or invalid text")
			}
			start := strings.Index(text, edit.OldText)
			if start < 0 || strings.Contains(text[start+1:], edit.OldText) {
				return nil, errors.New("old text must match exactly once")
			}
			end := start + len(edit.OldText)
			startLine := strings.Count(text[:start], "\n") + 1
			endLine := strings.Count(text[:end-1], "\n") + 1
			if !s.covered(file.Path, startLine, endLine) {
				return nil, errors.New("replacement includes unread source lines")
			}
			spans = append(spans, editSpan{start, end, edit.NewText})
		}
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
		for i := 1; i < len(spans); i++ {
			if spans[i].start < spans[i-1].end {
				return nil, errors.New("replacement spans overlap")
			}
		}
		for i := len(spans) - 1; i >= 0; i-- {
			p := spans[i]
			text = text[:p.start] + p.text + text[p.end:]
		}
		if newline == "crlf" {
			text = strings.ReplaceAll(text, "\n", "\r\n")
		}
		if int64(len(text)) > s.workspace.limits.MaxFileBytes {
			return nil, errors.New("replacement exceeds file byte limit")
		}
		contents[file.Path] = []byte(text)
	}
	patch, err := s.workspace.diffContents(ctx, s.revision, contents, limits.MaxPatchBytes)
	if err != nil {
		return nil, err
	}
	if _, err := s.workspace.ValidatePatch(ctx, patch, limits); err != nil {
		return nil, err
	}
	return patch, nil
}

func byteDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
