package coderepair

import (
	"errors"
	"strings"
)

// RuntimeSpec is an immutable server-selected engine/configuration snapshot.
// Only encrypted provider data is stored; it is excluded from public attempts.
type RuntimeSpec struct {
	EngineID          string `json:"engine_id"`
	EngineVersion     string `json:"engine_version"`
	ContractVersion   string `json:"contract_version"`
	CheckpointVersion string `json:"checkpoint_version"`
	ThreadID          string `json:"thread_id"`
	Profile           string `json:"profile"`
	ConfigVersion     string `json:"config_version"`
	CampaignID        string `json:"campaign_id"`
	PlaybookDigest    string `json:"playbook_digest"`
	SnapshotSHA256    string `json:"snapshot_sha256"`
	SealedSnapshot    string `json:"sealed_snapshot"`
}

func (s RuntimeSpec) Validate() error {
	for _, value := range []string{s.EngineID, s.EngineVersion, s.ContractVersion, s.CheckpointVersion,
		s.Profile, s.ConfigVersion, s.CampaignID} {
		if !safeEvidenceIdentifier(value) {
			return errors.New("invalid investigation runtime identity")
		}
	}
	if !validDigest(s.PlaybookDigest) || !validDigest(s.SnapshotSHA256) ||
		!strings.HasPrefix(s.SealedSnapshot, "v1.") || len(s.SealedSnapshot) > 32768 || len(s.ThreadID) > 256 {
		return errors.New("invalid sealed investigation runtime snapshot")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
