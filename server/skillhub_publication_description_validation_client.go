package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/openchat/openchat/server/store"
)

const publicationDescriptionValidationPath = "/api/skills/publication-descriptions/internal/validate"

var ErrSkillHubPublicationDescriptionValidationDisabled = errors.New("skillhub publication description validation is disabled")

// ReadValidationReport requests a report on the exact retained derived package,
// never on caller-supplied bytes or the original rejected candidate. It is not
// an execution grant; the trusted caller must bracket it with fresh application
// authority and original Runtime source checks. It retains no report or round.
func (c *PublicationDescriptionDerivationClient) ReadValidationReport(ctx context.Context, app store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
	if c == nil || !c.enabled || !c.validationEnabled {
		return nil, ErrSkillHubPublicationDescriptionValidationDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app = snapshotPublicationDescriptionApplication(app)
	if validatePublicationDescriptionCopyApplication(app) != nil || app.Input.Receipt.Query.Issuer != c.issuer || app.Input.Receipt.Query.Audience != c.audience {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	proposal := app.Input.Draft.Proposal
	if proposal.RelativePath != "SKILL.md" || proposal.EntryIndex == nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationUnsupported
	}
	request := struct {
		SchemaVersion   int                                                   `json:"schemaVersion"`
		ApplicationID   string                                                `json:"applicationId"`
		TaskID          string                                                `json:"taskId"`
		RequestHash     string                                                `json:"requestHash"`
		CandidateHash   string                                                `json:"candidateHash"`
		Proposal        store.SkillHubPublicationDescriptionProposal          `json:"proposal"`
		ExpectedReceipt store.SkillHubPublicationDescriptionDerivationReceipt `json:"expectedReceipt"`
		DerivedBytes    int64                                                 `json:"derivedBytes"`
	}{1, app.ID, app.Input.Receipt.TaskID, app.Input.Receipt.Query.RequestHash,
		app.Input.Draft.Evidence.CandidateHash, proposal, app.Result.Derivation, app.Result.RequestBytes}
	body, err := json.Marshal(request)
	if err != nil || len(body) > 32768 {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	endpoint := *c.endpoint
	endpoint.Path, endpoint.RawPath = publicationDescriptionValidationPath, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(resp.Header.Values("Content-Type")) != 1 || resp.Header.Get("Content-Type") != "application/json; charset=utf-8" ||
		len(resp.Header.Values("Content-Encoding")) != 0 || resp.Uncompressed || resp.ContentLength > 64*1024 {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(raw) > 64*1024 {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	parts, err := publicationExactObject(raw, "schemaVersion", "applicationId", "taskId", "derivedBytes", "receipt", "validation")
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	var out struct {
		SchemaVersion int                                                   `json:"schemaVersion"`
		ApplicationID string                                                `json:"applicationId"`
		TaskID        string                                                `json:"taskId"`
		DerivedBytes  int64                                                 `json:"derivedBytes"`
		Receipt       store.SkillHubPublicationDescriptionDerivationReceipt `json:"receipt"`
		Validation    json.RawMessage                                       `json:"validation"`
	}
	if decodeUniqueJSON(raw, &out) != nil || out.SchemaVersion != 1 || out.ApplicationID != app.ID || out.TaskID != app.Input.Receipt.TaskID ||
		out.DerivedBytes != app.Result.RequestBytes || out.Receipt != app.Result.Derivation {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if _, err := publicationExactObject(parts["receipt"], "schemaVersion", "algorithm", "originalRequestHash", "originalCandidateHash", "validationDigest", "proposalDigest", "limitsFingerprint", "derivedRequestHash", "derivedCandidateHash"); err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	report, err := decodePublicationValidationReportBody(out.Validation, app.Result.Derivation.DerivedCandidateHash)
	if err != nil || !validDescriptionDerivedReport(*report, app) {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return report, nil
}
