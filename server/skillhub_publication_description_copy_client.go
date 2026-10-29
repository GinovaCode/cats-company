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

const publicationDescriptionReadPath = "/api/skills/publication-descriptions/internal/read"

var ErrSkillHubPublicationDescriptionReadDisabled = errors.New("skillhub publication description copy read is disabled")

// ReadRetainedCopy recovers only the exact independent bytes previously accepted
// for this application. It does not derive, recapture, publish, or grant current
// authority. The caller must load the application from its current-authority
// store and verify the live Runtime source before AND after this network call.
func (c *PublicationDescriptionDerivationClient) ReadRetainedCopy(ctx context.Context, app store.SkillHubPublicationDescriptionApplication) ([]byte, error) {
	if c == nil || !c.enabled || !c.readEnabled {
		return nil, ErrSkillHubPublicationDescriptionReadDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app = snapshotPublicationDescriptionApplication(app)
	if err := validatePublicationDescriptionCopyApplication(app); err != nil || app.Input.Receipt.Query.Issuer != c.issuer || app.Input.Receipt.Query.Audience != c.audience {
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
	// The destination is derived exclusively from the constructor-validated
	// HTTPS peer endpoint. No application, browser or model can supply a URL.
	endpoint := *c.endpoint
	endpoint.Path, endpoint.RawPath = publicationDescriptionReadPath, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.client.Do(req)
	if err != nil {
		// Do not surface a peer's private URL/body or partial bytes in errors.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(resp.Header.Values("Content-Type")) != 1 || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		len(resp.Header.Values("Content-Encoding")) != 0 || resp.Uncompressed || len(resp.TransferEncoding) != 0 || resp.ContentLength != app.Result.RequestBytes {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, app.Result.RequestBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || int64(len(raw)) != app.Result.RequestBytes || publicationDescriptionSHA256Hex(string(raw)) != app.Result.Derivation.DerivedRequestHash {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}
