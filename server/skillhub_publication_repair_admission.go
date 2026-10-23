package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"github.com/openchat/openchat/server/store"
)

var publicationRepairTaskID = regexp.MustCompile(`^pubrepair_[a-f0-9]{32}$`)

func publicationRepairDigest(domain string, value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte(domain+"\x00"), b...))
	return hex.EncodeToString(h[:]), nil
}

// Accept only an already-verified peer report, never browser/model evidence.
// Sorting/deduplication removes issue ordering, fresh report IDs and display text
// from progress identity. The exact report digest still binds all those fields.
func publicationRepairEvidence(report PublicationValidationReport, candidate string) (store.SkillHubPublicationRepairEvidence, error) {
	if !validPublicationValidationReport(report, candidate) || report.Outcome != "rejected" || report.Truncated {
		return store.SkillHubPublicationRepairEvidence{}, store.ErrSkillHubPublicationInvalid
	}
	identities := make(map[string]struct{}, len(report.Issues))
	for _, issue := range report.Issues {
		b, err := json.Marshal(struct {
			Code         string                        `json:"code"`
			Category     string                        `json:"category"`
			Blocking     bool                          `json:"blocking"`
			Location     PublicationValidationLocation `json:"location"`
			RepairPolicy string                        `json:"repairPolicy"`
		}{issue.Code, issue.Category, issue.Blocking, issue.Location, issue.RepairPolicy})
		if err != nil {
			return store.SkillHubPublicationRepairEvidence{}, err
		}
		identities[string(b)] = struct{}{}
	}
	issues := make([]string, 0, len(identities))
	for identity := range identities {
		issues = append(issues, identity)
	}
	sort.Strings(issues)
	rh, err := publicationRepairDigest("catsco-publication-repair-report-v1", report)
	if err != nil {
		return store.SkillHubPublicationRepairEvidence{}, err
	}
	ih, err := publicationRepairDigest("catsco-publication-repair-issues-v1", issues)
	if err != nil {
		return store.SkillHubPublicationRepairEvidence{}, err
	}
	return store.SkillHubPublicationRepairEvidence{CandidateHash: candidate, ReportHash: rh, IssueSetHash: ih}, nil
}

// Admission records explicit human intent and zero spent rounds. It schedules
// nothing, never edits a candidate, and cannot approve a description or publish.
func (h *SkillHubPublicationHumanCommitHandler) admitRepairTask(ctx context.Context, w http.ResponseWriter, actor int64, original store.SkillHubPublicationCommitContext, report PublicationValidationReport, topic string) {
	if !h.repairAuthorityCurrent(ctx, actor, original) {
		skillHubPublicationReadError(w, 503, "publication.authority_changed")
		return
	}
	evidence, err := publicationRepairEvidence(report, original.Receipt.Query.SourceSnapshotHash)
	if err != nil {
		skillHubPublicationReadError(w, 409, "publication.complete_rejection_required")
		return
	}
	in := store.SkillHubPublicationRepairInput{Receipt: *original.Receipt, TopicID: topic, Evidence: evidence}
	task, err := h.repairs.CreateSkillHubPublicationRepairTask(ctx, actor, in)
	if err != nil {
		h.failure(w, err)
		return
	}
	if task == nil || task.Input != in || ctx.Err() != nil {
		skillHubPublicationReadError(w, 503, "publication.repair_uncertain")
		return
	}
	h.writeRepairTask(w, original, task)
}

func (h *SkillHubPublicationHumanCommitHandler) readRepairTask(ctx context.Context, w http.ResponseWriter, actor int64, original store.SkillHubPublicationCommitContext) {
	task, err := h.repairs.GetSkillHubPublicationRepairTask(ctx, actor, original.Entry.ID)
	if err != nil {
		h.failure(w, err)
		return
	}
	if !h.repairAuthorityCurrent(ctx, actor, original) {
		skillHubPublicationReadError(w, 503, "publication.authority_changed")
		return
	}
	if task == nil {
		skillHubPublicationReadError(w, 404, "publication.repair_not_found")
		return
	}
	h.writeRepairTask(w, original, task)
}

func (h *SkillHubPublicationHumanCommitHandler) writeRepairTask(w http.ResponseWriter, original store.SkillHubPublicationCommitContext, task *store.SkillHubPublicationRepairTask) {
	hash, err := task.Input.IdentityHash()
	if err != nil || task.IdentityHash != hash || !publicationRepairTaskID.MatchString(task.ID) || task.CreatedAt.IsZero() ||
		original.Receipt == nil || task.Input.Receipt != *original.Receipt || len(task.Rounds) > store.SkillHubPublicationRepairMaxRounds {
		skillHubPublicationReadError(w, 503, "publication.repair_uncertain")
		return
	}
	writeSkillHubPublicationJSON(w, 200, map[string]any{
		"schemaVersion": 1, "entryRequestId": original.Entry.ID,
		"botUid": strconv.FormatInt(original.Entry.Input.BotUID, 10), "topicId": task.Input.TopicID,
		"taskId": task.ID, "state": task.State(), "roundsUsed": len(task.Rounds),
		"maxFormatRepairRounds":  store.SkillHubPublicationRepairMaxRounds,
		"automaticRepairEnabled": false,
	})
}
