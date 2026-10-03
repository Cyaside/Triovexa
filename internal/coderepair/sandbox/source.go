package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

type SourceRange struct {
	Path      string `json:"path"`
	Revision  string `json:"revision"`
	Digest    string `json:"digest"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Text      string `json:"text"`
	Untrusted bool   `json:"untrusted"`
}

type SourceHit struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
}

type cachedSource struct {
	data   []byte
	digest string
	lines  []string
}

// SourceIndex belongs to one immutable checkout revision. It counts unique
// source bytes once and never shares code with another attempt or revision.
// Discard it before applying a candidate; post-patch source has a new digest.
type SourceIndex struct {
	workspace *Workspace
	revision  string
	mu        sync.Mutex
	files     map[string]cachedSource
	listings  map[string][]string
	accounted map[string]SourceCharge
	BytesRead int64
	Reads     int
}

func NewSourceIndex(workspace *Workspace, revision string) *SourceIndex {
	return &SourceIndex{workspace: workspace, revision: revision,
		files: make(map[string]cachedSource), listings: make(map[string][]string), accounted: make(map[string]SourceCharge)}
}

func (s *SourceIndex) source(name string) (cachedSource, error) {
	if source, ok := s.files[name]; ok {
		return source, nil
	}
	prior, known := s.accounted[name]
	if !known && len(s.accounted) >= s.workspace.limits.MaxListedFiles {
		return cachedSource{}, errors.New("source unique-file budget exhausted")
	}
	if err := s.workspace.checkedPath(name); err != nil {
		return cachedSource{}, err
	}
	info, err := s.workspace.root.Stat(name)
	if err != nil || !info.Mode().IsRegular() || (!known && info.Size() > s.workspace.limits.MaxTotalBytes-s.BytesRead) {
		return cachedSource{}, errors.New("source unique-byte budget exhausted or file invalid")
	}
	data, err := s.workspace.readIndexedFile(name)
	if err != nil {
		return cachedSource{}, err
	}
	digest := sha256.Sum256(data)
	source := cachedSource{data: data, digest: hex.EncodeToString(digest[:]), lines: strings.Split(string(data), "\n")}
	charge := SourceCharge{Digest: source.digest, Bytes: int64(len(data))}
	if known && prior != charge {
		return cachedSource{}, errors.New("source changed from the accounted approved revision")
	}
	if !known {
		if charge.Bytes > s.workspace.limits.MaxTotalBytes-s.BytesRead {
			return cachedSource{}, errors.New("source unique-byte budget exhausted")
		}
		s.accounted[name] = charge
		s.BytesRead += charge.Bytes
	}
	s.files[name] = source
	s.Reads++
	return source, nil
}

// SourceCharge and SourceAccounting contain no source content. They preserve
// the attempt's unique-file quota when a process or candidate cache restarts.
type SourceCharge struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

type SourceAccounting struct {
	Revision string                  `json:"revision"`
	Files    map[string]SourceCharge `json:"files"`
}

func (s *SourceIndex) Accounting() SourceAccounting {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := SourceAccounting{Revision: s.revision, Files: make(map[string]SourceCharge, len(s.accounted))}
	for name, charge := range s.accounted {
		result.Files[name] = charge
	}
	return result
}

func (s *SourceIndex) validateAccounting(state SourceAccounting) (int64, error) {
	if state.Revision != s.revision || !coderepair.ValidGitRevision(state.Revision) || state.Files == nil || len(state.Files) > s.workspace.limits.MaxListedFiles {
		return 0, errors.New("source accounting does not match the approved revision or file limit")
	}
	var bytes int64
	for name, charge := range state.Files {
		if !s.workspace.binding.AllowsPath(name) || len(charge.Digest) != 64 || strings.ToLower(charge.Digest) != charge.Digest || charge.Bytes < 0 || charge.Bytes > s.workspace.limits.MaxFileBytes || charge.Bytes > s.workspace.limits.MaxTotalBytes-bytes {
			return 0, errors.New("source accounting path, digest or byte limit is invalid")
		}
		if _, err := hex.DecodeString(charge.Digest); err != nil {
			return 0, errors.New("source accounting digest is invalid")
		}
		bytes += charge.Bytes
	}
	return bytes, nil
}

func (s *SourceIndex) ValidateAccounting(state SourceAccounting) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.validateAccounting(state)
	return err
}

func (s *SourceIndex) RestoreAccounting(state SourceAccounting) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bytes, err := s.validateAccounting(state)
	if err != nil {
		return err
	}
	for name, prior := range s.accounted {
		if next, exists := state.Files[name]; !exists || next != prior {
			return errors.New("source accounting cannot shrink or change within an attempt")
		}
	}
	s.accounted = make(map[string]SourceCharge, len(state.Files))
	for name, charge := range state.Files {
		s.accounted[name] = charge
	}
	s.BytesRead = bytes
	return nil
}

func (s *SourceIndex) ReadRange(name string, start, end int, expectedDigest string) (SourceRange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if start < 1 || end < start || end-start >= 500 {
		return SourceRange{}, errors.New("source range must contain at most 500 lines")
	}
	source, err := s.source(name)
	if err != nil {
		return SourceRange{}, err
	}
	if expectedDigest != "" && expectedDigest != source.digest {
		return SourceRange{}, errors.New("source digest does not match the approved revision")
	}
	if start > len(source.lines) {
		return SourceRange{}, errors.New("source range starts after end of file")
	}
	if end > len(source.lines) {
		end = len(source.lines)
	}
	text := strings.Join(source.lines[start-1:end], "\n")
	if len(text) > 32*1024 {
		return SourceRange{}, errors.New("source range exceeds 32 KiB")
	}
	return SourceRange{Path: name, Revision: s.revision, Digest: source.digest,
		StartLine: start, EndLine: end, Text: text, Untrusted: true}, nil
}

func pageBounds(cursor, limit, count int) (int, error) {
	if cursor < 0 || cursor > count || limit < 1 || limit > 20 {
		return 0, errors.New("page requires a valid cursor and limit between 1 and 20")
	}
	end := cursor + limit
	if end > count {
		end = count
	}
	return end, nil
}

func (s *SourceIndex) listing(prefix string) ([]string, error) {
	if names, ok := s.listings[prefix]; ok {
		return names, nil
	}
	names, err := s.workspace.ListFiles(prefix)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	s.listings[prefix] = names
	return names, nil
}

func (s *SourceIndex) List(prefix string, cursor, limit int) ([]string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.listing(prefix)
	if err != nil {
		return nil, 0, err
	}
	end, err := pageBounds(cursor, limit, len(names))
	if err != nil {
		return nil, 0, err
	}
	next := end
	if end == len(names) {
		next = 0
	}
	return append([]string(nil), names[cursor:end]...), next, nil
}

func (s *SourceIndex) Search(prefix, query string, cursor, limit int) ([]SourceHit, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if query == "" || len(query) > 256 || strings.IndexByte(query, 0) >= 0 {
		return nil, 0, errors.New("search requires a bounded literal query")
	}
	names, err := s.listing(prefix)
	if err != nil {
		return nil, 0, err
	}
	var hits []SourceHit
	for _, name := range names {
		source, err := s.source(name)
		if err != nil {
			return nil, 0, err
		}
		for index, line := range source.lines {
			if strings.Contains(line, query) {
				if len(hits) >= 1000 {
					return nil, 0, errors.New("search index hit bound exceeded")
				}
				if len(line) > 512 {
					line = line[:512]
				}
				hits = append(hits, SourceHit{Path: name, Digest: source.digest, Line: index + 1, Text: line})
			}
		}
	}
	end, err := pageBounds(cursor, limit, len(hits))
	if err != nil {
		return nil, 0, err
	}
	next := end
	if end == len(hits) {
		next = 0
	}
	return append([]SourceHit(nil), hits[cursor:end]...), next, nil
}
