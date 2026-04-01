package retrieval

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type FileRetriever struct {
	docsRoot string
}

type scoredDocument struct {
	path    string
	title   string
	content string
	score   int
}

func NewFileRetriever(docsRoot string) *FileRetriever {
	return &FileRetriever{docsRoot: docsRoot}
}

func (r *FileRetriever) Retrieve(ctx context.Context, incident domain.Incident, evidence []domain.EvidenceItem) ([]domain.DocumentReference, error) {
	_ = ctx

	keywords := buildKeywords(incident, evidence)
	var candidates []scoredDocument

	err := filepath.WalkDir(r.docsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}

		contentBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		content := strings.ToLower(string(contentBytes))
		filename := strings.ToLower(filepath.Base(path))
		score := 0
		for _, keyword := range keywords {
			if keyword == "" {
				continue
			}
			if strings.Contains(content, keyword) {
				score += 3
			}
			if strings.Contains(filename, keyword) {
				score += 5
			}
		}

		if score == 0 {
			return nil
		}

		candidates = append(candidates, scoredDocument{
			path:    path,
			title:   trimTitle(entry.Name()),
			content: string(contentBytes),
			score:   score,
		})

		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	limit := 3
	if len(candidates) < limit {
		limit = len(candidates)
	}

	references := make([]domain.DocumentReference, 0, limit)
	for _, candidate := range candidates[:limit] {
		references = append(references, domain.DocumentReference{
			ID:              uuid.NewString(),
			IncidentID:      incident.ID,
			DocumentTitle:   candidate.title,
			DocumentType:    inferDocumentType(candidate.path),
			RelevanceReason: buildRelevanceReason(candidate.score),
			Snippet:         firstParagraph(candidate.content),
		})
	}

	return references, nil
}

func buildKeywords(incident domain.Incident, evidence []domain.EvidenceItem) []string {
	keywords := []string{
		strings.ToLower(incident.ServiceName),
		strings.ToLower(incident.Environment),
		strings.ToLower(incident.Severity),
	}

	titleTokens := strings.Fields(strings.ToLower(incident.Title))
	for _, token := range titleTokens {
		if len(token) >= 4 {
			keywords = append(keywords, token)
		}
	}

	for _, item := range evidence {
		snippet := strings.ToLower(item.Snippet)
		switch {
		case strings.Contains(snippet, "worker"):
			keywords = append(keywords, "worker")
		case strings.Contains(snippet, "timeout"):
			keywords = append(keywords, "timeout")
		case strings.Contains(snippet, "cache"):
			keywords = append(keywords, "cache")
		case strings.Contains(snippet, "error rate"):
			keywords = append(keywords, "error")
		}
	}

	return keywords
}

func inferDocumentType(path string) string {
	switch {
	case strings.Contains(path, "runbooks"):
		return "runbook"
	case strings.Contains(path, "postmortems"):
		return "postmortem"
	default:
		return "doc"
	}
}

func trimTitle(filename string) string {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	name = strings.ReplaceAll(name, "-", " ")
	return strings.TrimSpace(name)
}

func buildRelevanceReason(score int) string {
	if score >= 12 {
		return "high keyword overlap with incident title and collected evidence"
	}
	return "matched multiple incident keywords from title and evidence"
}

func firstParagraph(content string) string {
	normalized := strings.TrimSpace(content)
	if normalized == "" {
		return ""
	}

	chunks := strings.Split(normalized, "\n\n")
	return strings.TrimSpace(chunks[0])
}
