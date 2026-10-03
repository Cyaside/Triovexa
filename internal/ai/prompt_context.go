package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/security"
)

const (
	maxContextItems        = 8
	maxContextSnippetBytes = 2048
	maxContextBytes        = 16 * 1024
)

// EvidenceContext renders bounded, explicitly untrusted evidence with stable
// references. It does not silently upgrade old or timestamp-free observations.
func EvidenceContext(items []domain.EvidenceItem) string {
	var lines []string
	total := 0
	for _, item := range items {
		if len(lines) == maxContextItems {
			break
		}
		text, truncated := boundedText(item.Snippet, maxContextSnippetBytes)
		observedAt := "unknown"
		if !item.Timestamp.IsZero() {
			observedAt = item.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		line := contextJSON(struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			Source     string `json:"source"`
			ObservedAt string `json:"observed_at"`
			Snippet    string `json:"snippet"`
			Truncated  bool   `json:"truncated"`
			Untrusted  bool   `json:"untrusted"`
		}{contextLabel(item.ID), contextLabel(item.Type), contextLabel(item.Source), observedAt, text, truncated, true})
		if total+len(line)+1 > maxContextBytes {
			break
		}
		lines = append(lines, line)
		total += len(line) + 1
	}
	return renderedContext(lines, len(items))
}

// DocumentContext includes the actual passage rather than a relevance label.
// The digest identifies the sanitized source passage; it is not a claim that
// the retriever has verified a repository revision or document freshness.
func DocumentContext(items []domain.DocumentReference) string {
	var lines []string
	total := 0
	for _, item := range items {
		if len(lines) == 5 {
			break
		}
		passage := strings.TrimSpace(security.Redact(item.Snippet))
		digest := sha256.Sum256([]byte(passage))
		text, truncated := boundedText(passage, maxContextSnippetBytes)
		line := contextJSON(struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			Type          string `json:"type"`
			Passage       string `json:"passage"`
			PassageSHA256 string `json:"passage_sha256"`
			Freshness     string `json:"freshness"`
			Truncated     bool   `json:"truncated"`
			Untrusted     bool   `json:"untrusted"`
		}{contextLabel(item.ID), contextLabel(item.DocumentTitle), contextLabel(item.DocumentType), text, hex.EncodeToString(digest[:]), "unverified", truncated, true})
		if total+len(line)+1 > maxContextBytes {
			break
		}
		lines = append(lines, line)
		total += len(line) + 1
	}
	return renderedContext(lines, len(items))
}

func boundedText(value string, maxBytes int) (string, bool) {
	value = strings.TrimSpace(security.Redact(value))
	if len(value) <= maxBytes {
		return value, false
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

func contextLabel(value string) string {
	text, _ := boundedText(value, 256)
	return text
}

func contextJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func renderedContext(lines []string, inputCount int) string {
	if inputCount == 0 {
		return "- none provided"
	}
	if len(lines) < inputCount {
		lines = append(lines, `{"omitted":true,"reason":"context_size_limit"}`)
	}
	return strings.Join(lines, "\n")
}
