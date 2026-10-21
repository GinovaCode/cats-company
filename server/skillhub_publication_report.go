package server

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

// The initial validation report is historical evidence about the captured
// candidate. Passed validation is neither human approval nor a published result.
type PublicationValidationReport struct {
	SchemaVersion     string                       `json:"schemaVersion"`
	RuleVersion       string                       `json:"ruleVersion"`
	Profile           string                       `json:"profile"`
	ReportID          string                       `json:"reportId"`
	Outcome           string                       `json:"outcome"`
	Coverage          string                       `json:"coverage"`
	CandidateHash     string                       `json:"candidateHash"`
	LimitsFingerprint string                       `json:"limitsFingerprint"`
	Issues            []PublicationValidationIssue `json:"issues"`
	Truncated         bool                         `json:"truncated"`
	NextAction        string                       `json:"nextAction"`
}

type PublicationValidationIssue struct {
	Code         string                        `json:"code"`
	Category     string                        `json:"category"`
	Blocking     bool                          `json:"blocking"`
	Location     PublicationValidationLocation `json:"location"`
	Message      string                        `json:"message"`
	RepairPolicy string                        `json:"repairPolicy"`
}

type PublicationValidationLocation struct {
	EntryIndex   *int64 `json:"entryIndex,omitempty"`
	RelativePath string `json:"relativePath,omitempty"`
	Field        string `json:"field,omitempty"`
}

type PublicationCloudValidationReport struct {
	SchemaVersion int                            `json:"schemaVersion"`
	Receipt       publicationCloudReceiptRequest `json:"receipt"`
	Validation    PublicationValidationReport    `json:"validation"`
}

var publicationReportID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
var publicationRuleVersion = regexp.MustCompile(`^skillhub-publish-v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)
var publicationIssueCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}\.[a-z][a-z0-9_]{0,63}$`)
var publicationReportHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validPublicationValidationReport(p PublicationValidationReport, candidate string) bool {
	if p.SchemaVersion != "1" || !publicationRuleVersion.MatchString(p.RuleVersion) || p.Profile != "skillhub-publish-v1" ||
		!publicationReportID.MatchString(p.ReportID) || p.Coverage != "complete" || !publicationReportHash.MatchString(p.CandidateHash) ||
		p.CandidateHash != candidate || !publicationReportHash.MatchString(p.LimitsFingerprint) || p.Issues == nil || len(p.Issues) > 50 {
		return false
	}
	if (p.Outcome != "passed" || p.NextAction != "none") && (p.Outcome != "rejected" || p.NextAction != "review_report") {
		return false
	}
	blocking := false
	for _, i := range p.Issues {
		if !publicationIssueCode.MatchString(i.Code) || (i.Category != "format" && i.Category != "package_safety" && i.Category != "compatibility") ||
			(i.RepairPolicy != "manual" && i.RepairPolicy != "confirmation_required" && i.RepairPolicy != "none") ||
			(i.Code == "metadata.description_missing" && i.RepairPolicy != "confirmation_required") ||
			i.Message == "" || len(i.Message) > 1024 || !utf8.ValidString(i.Message) || strings.ContainsFunc(i.Message, func(r rune) bool { return r < 32 || r == 127 }) {
			return false
		}
		l := i.Location
		if (l.EntryIndex != nil && (*l.EntryIndex < 0 || *l.EntryIndex > 9007199254740991)) ||
			(l.RelativePath != "" && l.RelativePath != "SKILL.md" && l.RelativePath != "skill.json" && l.RelativePath != "README.md") {
			return false
		}
		switch l.Field {
		case "", "name", "id", "displayName", "description", "version", "entrypoints", "entrypoints.skillFile", "runtime", "platforms", "runtime.platforms", "manifest":
		default:
			return false
		}
		blocking = blocking || i.Blocking
	}
	if (p.Outcome == "passed" && blocking) || (p.Outcome == "rejected" && !blocking && !p.Truncated) {
		return false
	}
	// Match the validator's pretty-printed 48 KiB response budget. HTML escaping
	// is conservative here; no raw report or candidate is sent on failure.
	b, err := json.MarshalIndent(map[string]any{"validation": p}, "", "  ")
	return err == nil && len(b)+1 <= 48*1024
}

func decodePublicationValidationReport(data []byte) (*PublicationCloudValidationReport, error) {
	o, err := publicationExactObject(data, "schemaVersion", "receipt", "validation")
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	if _, err = decodePublicationCloudReceipt(o["receipt"]); err != nil {
		return nil, errPublicationCommitCloud
	}
	p, err := publicationExactObject(o["validation"], "schemaVersion", "ruleVersion", "profile", "reportId", "outcome", "coverage", "candidateHash", "limitsFingerprint", "issues", "truncated", "nextAction")
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
	var out PublicationCloudValidationReport
	if json.Unmarshal(data, &out) != nil || out.SchemaVersion != 1 || !validPublicationValidationReport(out.Validation, out.Receipt.SourceSnapshotHash) {
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
