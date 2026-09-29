package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Cyaside/Triovexa/internal/approval"
	"github.com/Cyaside/Triovexa/internal/coderepair"
	repairverify "github.com/Cyaside/Triovexa/internal/coderepair/verification"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func registerRepairRoutes(mux *http.ServeMux, cfg config.Config, repository storage.Repository, runtime *RuntimeControls, approvalService *approval.Service) {
	registerRepairAPI(mux, cfg, repository, runtime, approvalService)
	store, _ := repository.(interface {
		ApplyRepairPREvent(context.Context, coderepair.PREvent) (bool, error)
	})
	mux.HandleFunc("/webhooks/github/repair", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
			return
		}
		if store == nil || cfg.RepairGitHubWebhookSecret == "" {
			writeAPIError(w, http.StatusServiceUnavailable, "repair_webhook_unavailable", "Repair webhook is not configured.")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "invalid_webhook", "Webhook payload is too large.")
			return
		}
		signature := strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
		provided, err := hex.DecodeString(signature)
		mac := hmac.New(sha256.New, []byte(cfg.RepairGitHubWebhookSecret))
		mac.Write(body)
		if err != nil || len(provided) != sha256.Size || !hmac.Equal(provided, mac.Sum(nil)) {
			writeAPIError(w, http.StatusUnauthorized, "invalid_webhook_signature", "Webhook signature is invalid.")
			return
		}
		delivery := r.Header.Get("X-GitHub-Delivery")
		if _, err := uuid.Parse(delivery); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_delivery", "Webhook delivery ID is invalid.")
			return
		}
		if r.Header.Get("X-GitHub-Event") != "pull_request" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var payload struct {
			Action     string `json:"action"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			PullRequest struct {
				Number         int64  `json:"number"`
				Merged         bool   `json:"merged"`
				MergeCommitSHA string `json:"merge_commit_sha"`
				Head           struct {
					Ref string `json:"ref"`
					SHA string `json:"sha"`
				} `json:"head"`
				Base struct {
					Ref string `json:"ref"`
				} `json:"base"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_webhook", "Webhook payload is invalid.")
			return
		}
		if payload.Action != "closed" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		applied, err := store.ApplyRepairPREvent(r.Context(), coderepair.PREvent{
			DeliveryID: delivery, Repository: payload.Repository.FullName,
			Branch: payload.PullRequest.Head.Ref, BaseRef: payload.PullRequest.Base.Ref,
			HeadSHA: payload.PullRequest.Head.SHA, Number: payload.PullRequest.Number,
			Merged: payload.PullRequest.Merged, MergeSHA: payload.PullRequest.MergeCommitSHA,
			ReceivedAt: time.Now().UTC(),
		})
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_repair_delivery", "Repair PR delivery was rejected.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"applied": applied})
	})
	deploymentStore, _ := repository.(interface {
		StartRepairDeployment(context.Context, string, string, string, string, repairverify.Sample, time.Time) (bool, error)
		CompleteRepairDeployment(context.Context, string, string, string, string, time.Time) (bool, error)
	})
	mux.HandleFunc("/webhooks/deployment/repair", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
			return
		}
		if deploymentStore == nil || cfg.RepairDeploymentToken == "" || cfg.WorkloadControlToken == "" {
			writeAPIError(w, http.StatusServiceUnavailable, "repair_deployment_unavailable", "Repair deployment correlation is not configured.")
			return
		}
		if !secureEqual(bearerToken(r), cfg.RepairDeploymentToken) {
			writeAPIError(w, http.StatusUnauthorized, "invalid_deployment_credential", "Deployment credential is invalid.")
			return
		}
		var body struct {
			CaseID       string `json:"case_id"`
			DeploymentID string `json:"deployment_id"`
			Environment  string `json:"environment"`
			RevisionSHA  string `json:"revision_sha"`
			Phase        string `json:"phase"`
		}
		if err := decodeBoundedJSON(w, r, &body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		now := time.Now().UTC()
		var applied bool
		var err error
		switch body.Phase {
		case "started":
			client, clientErr := repairverify.NewWorkloadClient(cfg.WorkloadControlBaseURL, cfg.WorkloadControlToken, cfg.PrometheusBaseURL)
			if clientErr != nil {
				writeAPIError(w, http.StatusServiceUnavailable, "telemetry_unavailable", "Workload telemetry is not configured.")
				return
			}
			baseline, fetchErr := client.Snapshot(r.Context())
			if fetchErr != nil {
				writeAPIError(w, http.StatusServiceUnavailable, "telemetry_unavailable", "A fresh pre-deployment baseline is required.")
				return
			}
			applied, err = deploymentStore.StartRepairDeployment(r.Context(), body.CaseID, body.DeploymentID, body.Environment, body.RevisionSHA, baseline, now)
		case "completed":
			applied, err = deploymentStore.CompleteRepairDeployment(r.Context(), body.CaseID, body.DeploymentID, body.Environment, body.RevisionSHA, now)
		default:
			writeAPIError(w, http.StatusBadRequest, "invalid_phase", "Deployment phase must be started or completed.")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusConflict, "deployment_rejected", "Deployment does not match the merged repair PR or current case state.")
			return
		}
		if !applied {
			writeAPIError(w, http.StatusConflict, "state_changed", "Repair case state changed.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "phase": body.Phase})
	})
}
