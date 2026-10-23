package server

import (
	"context"
	"net/http"
	"strconv"

	"github.com/openchat/openchat/server/store"
)

// The future trusted executor must fetch the complete report from its pinned
// peer and revalidate live source before proposing. This helper is not exposed
// as an HTTP/RPC/model tool and does not generate text or call a paid model.
func proposePublicationDescription(ctx context.Context, db store.SkillHubPublicationRepairDescriptionStore, actor int64, lease store.SkillHubPublicationRepairLease, report PublicationValidationReport, proposal store.SkillHubPublicationDescriptionProposal) (*store.SkillHubPublicationDescriptionDraft, error) {
	if db == nil || actor <= 0 || lease.Validate() != nil || proposal.Validate() != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	evidence, err := publicationRepairEvidence(report, lease.Input.Evidence.CandidateHash)
	if err != nil || evidence != lease.Input.Evidence {
		return nil, store.ErrSkillHubPublicationConflict
	}
	matched := false
	for _, issue := range report.Issues {
		if issue.Category == "package_safety" || (issue.Blocking && (issue.Code != "metadata.description_missing" || issue.Category != "format" || issue.RepairPolicy != "confirmation_required")) {
			return nil, store.ErrSkillHubPublicationDenied
		}
		l := issue.Location
		sameIndex := l.EntryIndex == nil && proposal.EntryIndex == nil
		if l.EntryIndex != nil && proposal.EntryIndex != nil {
			sameIndex = *l.EntryIndex == *proposal.EntryIndex
		}
		if issue.Blocking && issue.Code == "metadata.description_missing" && issue.RepairPolicy == "confirmation_required" &&
			l.RelativePath == proposal.RelativePath && l.Field == proposal.Field && sameIndex {
			matched = true
		}
	}
	if !matched {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	return db.ProposeSkillHubPublicationDescription(ctx, actor, lease, proposal)
}

func (h *SkillHubPublicationHumanCommitHandler) handleRepairDescription(ctx context.Context, w http.ResponseWriter, actor int64, original store.SkillHubPublicationCommitContext, readOnly bool, key string) {
	// The store rechecks current private-task authority transactionally. Reading
	// the original report or possessing a confirmation digest grants no access.
	task, err := h.repairs.GetSkillHubPublicationRepairTask(ctx, actor, original.Entry.ID)
	if err != nil {
		h.failure(w, err)
		return
	}
	if task == nil {
		skillHubPublicationReadError(w, 404, "publication.repair_not_found")
		return
	}
	var draft *store.SkillHubPublicationDescriptionDraft
	if readOnly {
		draft, err = h.descriptions.GetSkillHubPublicationDescription(ctx, actor, original.Entry.ID)
	} else {
		draft, err = h.descriptions.ConfirmSkillHubPublicationDescription(ctx, actor, original.Entry.ID, key)
	}
	if err != nil {
		h.failure(w, err)
		return
	}
	if !h.repairAuthorityCurrent(ctx, actor, original) {
		skillHubPublicationReadError(w, 503, "publication.authority_changed")
		return
	}
	if draft == nil {
		skillHubPublicationReadError(w, 404, "publication.description_not_found")
		return
	}
	if task.Input.Receipt != *original.Receipt || draft.ValidateFor(*task) != nil ||
		(!readOnly && (draft.ConfirmationKey != key || draft.ConfirmedAt == nil)) || ctx.Err() != nil {
		skillHubPublicationReadError(w, 503, "publication.description_uncertain")
		return
	}
	state := "awaiting_confirmation"
	if draft.ConfirmedAt != nil {
		state = "confirmed"
	}
	location := map[string]any{"relativePath": draft.Input.Proposal.RelativePath, "field": draft.Input.Proposal.Field}
	if draft.Input.Proposal.EntryIndex != nil {
		location["entryIndex"] = *draft.Input.Proposal.EntryIndex
	}
	writeSkillHubPublicationJSON(w, 200, map[string]any{
		"schemaVersion": 1, "entryRequestId": original.Entry.ID, "botUid": strconv.FormatInt(original.Entry.Input.BotUID, 10),
		"topicId": task.Input.TopicID, "taskId": task.ID, "automaticRepairEnabled": false,
		"draft": map[string]any{"confirmationKey": draft.ConfirmationKey, "round": draft.Input.Round, "location": location,
			"text": draft.Input.Proposal.Text, "state": state},
	})
}
