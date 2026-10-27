package server

import (
	"context"
	"encoding/json"

	"github.com/openchat/openchat/server/store"
)

// Aliases keep the existing wire contract and share validation with persistence.
type PublicationValidationReport = store.PublicationValidationReport
type PublicationValidationIssue = store.PublicationValidationIssue
type PublicationValidationLocation = store.PublicationValidationLocation

type PublicationCloudValidationReport struct {
	SchemaVersion int                            `json:"schemaVersion"`
	Receipt       publicationCloudReceiptRequest `json:"receipt"`
	Validation    PublicationValidationReport    `json:"validation"`
}

func validPublicationValidationReport(p PublicationValidationReport, candidate string) bool {
	return store.ValidPublicationValidationReport(p, candidate)
}

func decodePublicationValidationReport(data []byte) (*PublicationCloudValidationReport, error) {
	o, err := publicationExactObject(data, "schemaVersion", "receipt", "validation")
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	if _, err = decodePublicationCloudReceipt(o["receipt"]); err != nil {
		return nil, errPublicationCommitCloud
	}
	var out PublicationCloudValidationReport
	if json.Unmarshal(data, &out) != nil || out.SchemaVersion != 1 {
		return nil, errPublicationCommitCloud
	}
	report, err := decodePublicationValidationReportBody(o["validation"], out.Receipt.SourceSnapshotHash)
	if err != nil {
		return nil, err
	}
	out.Validation = *report
	return &out, nil
}

// Shared strict report decoder for original and independently derived candidates.
// Callers bind candidateHash using their own retained, authority-checked receipt.
func decodePublicationValidationReportBody(data []byte, candidateHash string) (*PublicationValidationReport, error) {
	p, err := publicationExactObject(data, "schemaVersion", "ruleVersion", "profile", "reportId", "outcome", "coverage", "candidateHash", "limitsFingerprint", "issues", "truncated", "nextAction")
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	var issues []json.RawMessage
	if json.Unmarshal(p["issues"], &issues) != nil || issues == nil || len(issues) > 50 {
		return nil, errPublicationCommitCloud
	}
	for _, raw := range issues {
		i, e := publicationExactObject(raw, "code", "category", "blocking", "location", "message", "repairPolicy")
		if e != nil {
			return nil, errPublicationCommitCloud
		}
		var locations map[string]json.RawMessage
		if json.Unmarshal(i["location"], &locations) != nil || locations == nil {
			return nil, errPublicationCommitCloud
		}
		keys := make([]string, 0, len(locations))
		for k := range locations {
			if k != "entryIndex" && k != "relativePath" && k != "field" {
				return nil, errPublicationCommitCloud
			}
			if (k == "relativePath" || k == "field") && string(locations[k]) == `""` {
				return nil, errPublicationCommitCloud
			}
			keys = append(keys, k)
		}
		if _, e = publicationExactObject(i["location"], keys...); e != nil {
			return nil, errPublicationCommitCloud
		}
	}
	var out PublicationValidationReport
	if json.Unmarshal(data, &out) != nil || !validPublicationValidationReport(out, candidateHash) {
		return nil, errPublicationCommitCloud
	}
	return &out, nil
}

// Report performs no validation run, retry, plan, approval or repair. It uses
// only the persisted, locally authenticated receipt and the pinned peer.
func (c *PublicationCommitCloudClient) Report(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
	data, err := c.send(ctx, r, "report", false)
	if err != nil {
		return nil, err
	}
	out, err := decodePublicationValidationReport(data)
	if err != nil || out.Receipt != publicationControlReceipt(r) {
		return nil, errPublicationCommitCloud
	}
	return out, nil
}
