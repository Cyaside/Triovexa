package security

import (
	"context"
	"log/slog"
	"regexp"
)

const redacted = "[REDACTED]"

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+\-/=]+`),
	regexp.MustCompile(`(?i)(://)[^/@\s:]+:[^/@\s]+@`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|password|secret)(\s*[=:]\s*|%3[dD])[^&\s,}\"]+`),
	regexp.MustCompile(`(?i)(\"(?:api[_-]?key|token|password|secret)\"\s*:\s*\")[^\"]+`),
}

func Redact(value string) string {
	result := value
	result = sensitivePatterns[0].ReplaceAllString(result, `${1}`+redacted)
	result = sensitivePatterns[1].ReplaceAllString(result, `${1}`+redacted+`@`)
	result = sensitivePatterns[2].ReplaceAllString(result, `${1}${2}`+redacted)
	result = sensitivePatterns[3].ReplaceAllString(result, `${1}`+redacted)
	return result
}

type RedactingHandler struct{ next slog.Handler }

func NewRedactingHandler(next slog.Handler) slog.Handler { return RedactingHandler{next: next} }

func (h RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	copy := slog.NewRecord(record.Time, record.Level, Redact(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		copy.AddAttrs(redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, copy)
}

func (h RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redactedAttrs := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		redactedAttrs[i] = redactAttr(attr)
	}
	return RedactingHandler{next: h.next.WithAttrs(redactedAttrs)}
}

func (h RedactingHandler) WithGroup(name string) slog.Handler {
	return RedactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(attr slog.Attr) slog.Attr {
	if attr.Value.Kind() == slog.KindGroup {
		items := attr.Value.Group()
		for i := range items {
			items[i] = redactAttr(items[i])
		}
		return slog.Group(attr.Key, attrsToAny(items)...)
	}
	if attr.Value.Kind() == slog.KindString {
		attr.Value = slog.StringValue(Redact(attr.Value.String()))
	}
	return attr
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for i := range attrs {
		values[i] = attrs[i]
	}
	return values
}
