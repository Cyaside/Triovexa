package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/domain"
)

type HeuristicGenerator struct{}

func NewHeuristicGenerator() *HeuristicGenerator {
	return &HeuristicGenerator{}
}

func (g *HeuristicGenerator) Generate(
	ctx context.Context,
	incident domain.Incident,
	evidence []domain.EvidenceItem,
	documents []domain.DocumentReference,
) (domain.TriageResult, error) {
	_ = ctx

	mode := detectMode(evidence)
	lastDeploy := extractMetadataField(evidence, "last_deploy")
	if lastDeploy == "" {
		lastDeploy = "unknown"
	}

	summary, hypotheses, blastRadius, nextSteps, confidence := summarizeIncident(incident, mode, lastDeploy, documents)

	return domain.TriageResult{
		ID:                uuid.NewString(),
		IncidentID:        incident.ID,
		Summary:           summary,
		Hypotheses:        hypotheses,
		BlastRadius:       blastRadius,
		NextSteps:         nextSteps,
		DraftStatusUpdate: fmt.Sprintf("[%s] %s", strings.ToUpper(incident.Severity), summary),
		ConfidenceNotes:   confidence,
		CreatedAt:         time.Now().UTC(),
	}, nil
}

func detectMode(evidence []domain.EvidenceItem) string {
	for _, item := range evidence {
		snippet := strings.ToLower(item.Snippet)
		switch {
		case strings.Contains(snippet, "worker stalled"):
			return "worker_stall"
		case strings.Contains(snippet, "request timeouts increased"):
			return "timeout_after_deploy"
		case strings.Contains(snippet, "error rate spike"):
			return "error_rate_spike"
		}
	}

	return "unknown"
}

func summarizeIncident(
	incident domain.Incident,
	mode string,
	lastDeploy string,
	documents []domain.DocumentReference,
) (string, []string, string, []string, string) {
	docHint := "Tidak ada dokumen kuat yang cocok."
	if len(documents) > 0 {
		docHint = fmt.Sprintf("Dokumen paling relevan: %s.", documents[0].DocumentTitle)
	}

	switch mode {
	case "worker_stall":
		return fmt.Sprintf("%s di %s menunjukkan backlog pekerjaan yang meningkat dan worker tidak sehat. %s", incident.ServiceName, incident.Environment, docHint),
			[]string{
				"worker demo mengalami stall atau crash loop sehingga antrean job tidak terproses",
				"deploy terakhir mungkin memicu inkompatibilitas payload dengan worker",
			},
			"blast radius utama ada pada background processing, namun request synchronous bisa ikut terdampak jika backlog terus bertambah",
			[]string{
				"cek health worker dan pola restart pada log",
				"bandingkan backlog sebelum dan sesudah alert",
				"review runbook restart worker atau retry background job yang cocok",
			},
			"confidence sedang ke tinggi karena backlog, health worker, dan dokumen retrieval mengarah ke pola yang konsisten"
	case "timeout_after_deploy":
		return fmt.Sprintf("%s di %s mengalami timeout setelah deploy %s dengan lonjakan latency yang jelas. %s", incident.ServiceName, incident.Environment, lastDeploy, docHint),
			[]string{
				"deploy terakhir memperkenalkan perubahan yang memperlambat dependency atau internal timeout budget",
				"error rate naik sebagai dampak sekunder dari request yang menunggu terlalu lama",
			},
			"jalur request utama kemungkinan terdampak, terutama operasi yang bergantung pada dependency lambat",
			[]string{
				"validasi apakah error meningkat tepat setelah deploy terakhir",
				"cek log timeout dan dependency yang paling sering muncul",
				"review postmortem timeout after deploy dan runbook rollback terkait",
			},
			"confidence tinggi karena evidence deploy dan pola timeout saling menguatkan"
	default:
		return fmt.Sprintf("%s di %s menunjukkan anomali error rate dan latency yang perlu ditriase lebih lanjut. %s", incident.ServiceName, incident.Environment, docHint),
			[]string{
				"ada degradasi performa pada jalur request utama",
				"anomali bisa terkait deploy terbaru atau kondisi dependency eksternal",
			},
			"kemungkinan berdampak pada pengguna service utama dan beberapa proses background terkait",
			[]string{
				"cek evidence metric dan log paling baru",
				"bandingkan anomali dengan deploy context terbaru",
				"review runbook atau postmortem dengan keyword yang sama",
			},
			"confidence sedang karena evidence awal cukup kuat tetapi mode insiden belum terlalu spesifik"
	}
}

func extractMetadataField(items []domain.EvidenceItem, key string) string {
	for _, item := range items {
		if item.MetadataJSON == "" {
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(item.MetadataJSON), &payload); err != nil {
			continue
		}

		if value, ok := payload[key]; ok {
			if typed, ok := value.(string); ok {
				return typed
			}
		}
	}

	return ""
}
