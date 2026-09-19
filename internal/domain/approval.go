package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func ActionApprovalDigest(action CandidateAction, policyVersion string) string {
	value := strings.Join([]string{
		action.ID, action.ActionType, action.TargetResource, action.ParametersJSON,
		string(action.RiskLevel), strings.TrimSpace(policyVersion),
	}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
