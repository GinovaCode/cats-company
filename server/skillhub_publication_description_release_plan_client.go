package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/openchat/openchat/server/store"
)

const publicationDescriptionPlanPath = "/api/skills/publication-descriptions/internal/plan"

var ErrSkillHubPublicationDescriptionPlanDisabled = errors.New("skillhub publication description planning is disabled")

// Internal planning facts only. The unchanged original upload receipt is NOT a
// commit input for the derived target; never send this through the old commit
// client. A distinct durable candidate-bound authorization is still required.
type publicationDescriptionReleasePlan struct {
	SchemaVersion      int                                                   `json:"schemaVersion"`
	ApplicationID      string                                                `json:"applicationId"`
	TaskID             string                                                `json:"taskId"`
	PublicationReceipt publicationCloudReceiptRequest                        `json:"publicationReceipt"`
	Receipt            store.SkillHubPublicationDescriptionDerivationReceipt `json:"receipt"`
	DerivedBytes       int64                                                 `json:"derivedBytes"`
	Target             store.SkillHubPublicationCommitTarget                 `json:"target"`
}

func (p publicationDescriptionReleasePlan) validateFor(app store.SkillHubPublicationDescriptionApplication, confirm bool) error {
	if validatePublicationDescriptionCopyApplication(app) != nil || p.SchemaVersion != 1 || p.ApplicationID != app.ID ||
		p.TaskID != app.Input.Receipt.TaskID || p.PublicationReceipt != publicationControlReceipt(app.Input.Receipt) ||
		p.Receipt != app.Result.Derivation || p.DerivedBytes != app.Result.RequestBytes || p.Target.Validate() != nil ||
		p.Target.CandidateHash != app.Result.Derivation.DerivedCandidateHash || p.Target.ConfirmVersionPublish != confirm {
		return store.ErrSkillHubPublicationConflict
	}
	return nil
}

// PlanRetainedCopy reserves/replays a Cloud target without granting permission,
// publishing, mutating the original candidate, or retrying a paid operation.
// The trusted coordinator must bracket this with current authority/source checks.
func (c *PublicationDescriptionDerivationClient) PlanRetainedCopy(ctx context.Context, app store.SkillHubPublicationDescriptionApplication, confirm bool) (*publicationDescriptionReleasePlan, error) {
	if c == nil || !c.enabled || !c.planEnabled {
		return nil, ErrSkillHubPublicationDescriptionPlanDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app = snapshotPublicationDescriptionApplication(app)
	if validatePublicationDescriptionCopyApplication(app) != nil || app.Input.Receipt.Query.Issuer != c.issuer || app.Input.Receipt.Query.Audience != c.audience {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	if app.Input.Draft.Proposal.RelativePath != "SKILL.md" || app.Input.Draft.Proposal.EntryIndex == nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationUnsupported
	}
	if c.planActive.Add(1) > 2 {
		c.planActive.Add(-1)
		return nil, store.ErrSkillHubPublicationBusy
	}
	defer c.planActive.Add(-1)
	request := struct {
		Copy struct {
			SchemaVersion   int                                                   `json:"schemaVersion"`
			ApplicationID   string                                                `json:"applicationId"`
			TaskID          string                                                `json:"taskId"`
			RequestHash     string                                                `json:"requestHash"`
			CandidateHash   string                                                `json:"candidateHash"`
			Proposal        store.SkillHubPublicationDescriptionProposal          `json:"proposal"`
			ExpectedReceipt store.SkillHubPublicationDescriptionDerivationReceipt `json:"expectedReceipt"`
			DerivedBytes    int64                                                 `json:"derivedBytes"`
		} `json:"copy"`
		PublicationReceipt    publicationCloudReceiptRequest `json:"publicationReceipt"`
		ConfirmVersionPublish bool                           `json:"confirmVersionPublish"`
	}{PublicationReceipt: publicationControlReceipt(app.Input.Receipt), ConfirmVersionPublish: confirm}
	request.Copy.SchemaVersion, request.Copy.ApplicationID, request.Copy.TaskID = 1, app.ID, app.Input.Receipt.TaskID
	request.Copy.RequestHash, request.Copy.CandidateHash = app.Input.Receipt.Query.RequestHash, app.Input.Draft.Evidence.CandidateHash
	request.Copy.Proposal, request.Copy.ExpectedReceipt, request.Copy.DerivedBytes = app.Input.Draft.Proposal, app.Result.Derivation, app.Result.RequestBytes
	body, err := json.Marshal(request)
	if err != nil || len(body) > 32768 {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	endpoint := *c.endpoint
	endpoint.Path, endpoint.RawPath = publicationDescriptionPlanPath, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	defer resp.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Cache-Control"} {
		if len(resp.Header.Values(key)) > 1 {
			return nil, ErrSkillHubPublicationDescriptionDerivationPeer
		}
	}
	if resp.Header.Get("Content-Type") != "application/json; charset=utf-8" || len(resp.Header.Values("Content-Encoding")) != 0 ||
		resp.Uncompressed || resp.ContentLength > 16384 {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	noStore := false
	for _, directive := range strings.Split(resp.Header.Get("Cache-Control"), ",") {
		noStore = noStore || strings.EqualFold(strings.TrimSpace(directive), "no-store")
	}
	if !noStore {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(raw) == 0 || len(raw) > 16384 {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if resp.StatusCode != http.StatusOK {
		// Only a fixed, exact version-policy error can request confirmation.
		// Never expose upstream text/URLs or treat an error as automatic repair.
		parts, err := publicationExactObject(raw, "error")
		if err == nil {
			e, err := publicationExactObject(parts["error"], "code", "message")
			var code, message string
			if err == nil && json.Unmarshal(e["code"], &code) == nil && json.Unmarshal(e["message"], &message) == nil &&
				resp.StatusCode == http.StatusConflict && code == "skillhub.publication_description_derivation_version_confirmation_required" {
				return nil, errPublicationCommitConfirmationRequired
			}
		}
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	parts, err := publicationExactObject(raw, "schemaVersion", "applicationId", "taskId", "publicationReceipt", "receipt", "derivedBytes", "target")
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if _, err := decodePublicationCloudReceipt(parts["publicationReceipt"]); err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if _, err := publicationExactObject(parts["receipt"], "schemaVersion", "algorithm", "originalRequestHash", "originalCandidateHash", "validationDigest", "proposalDigest", "limitsFingerprint", "derivedRequestHash", "derivedCandidateHash"); err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if _, err := publicationExactObject(parts["target"], "candidateHash", "manifestHash", "publisherUserId", "publisherProfileId", "namespace", "skillId", "version", "uploadId", "releaseAt", "confirmVersionPublish"); err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	var out publicationDescriptionReleasePlan
	if decodeUniqueJSON(raw, &out) != nil || out.validateFor(app, confirm) != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &out, nil
}
