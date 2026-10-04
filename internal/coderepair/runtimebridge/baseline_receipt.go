package runtimebridge

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

var errBaselineReceipt = errors.New("durable baseline is uncertain")

// This receipt is Go-owned and is not in the model tool inventory. Persisting
// the exact baseline before inference keeps checkpoint identity and paid-call
// payloads stable even when a repeated Go test would print a different duration.
func (r *toolReceipts) beginBaseline(ctx context.Context, s *ToolSession) (coderepair.ToolReceipt, error) {
	args, err := json.Marshal(map[string]string{"recipe_id": s.recipeID})
	if err != nil {
		return coderepair.ToolReceipt{}, err
	}
	digest, err := toolArgumentDigest(ToolRequest{Args: args})
	if err != nil {
		return coderepair.ToolReceipt{}, err
	}
	const id = "go-baseline"
	if receipt, exists := r.past[id]; exists {
		if receipt.Name != "baseline" || receipt.ArgsSHA256 != digest || receipt.State != "completed" || receipt.Revision != s.sourceRevision {
			return receipt, errBaselineReceipt
		}
		return receipt, nil
	}
	receipt, err := r.store.StartRepairTool(ctx, r.claim, coderepair.ToolReceipt{
		AttemptID: r.claim.Job.AttemptID, CallID: id, Name: "baseline", ArgsSHA256: digest, Revision: s.sourceRevision, State: "started"})
	if err != nil || receipt.State != "started" {
		return receipt, errBaselineReceipt
	}
	return receipt, nil
}
