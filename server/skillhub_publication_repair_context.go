package server

import (
	"context"
	"net/http"
	"strconv"

	"github.com/openchat/openchat/server/store"
)

// A bounded data snapshot for a future repair conversation, never an execution
// grant or model prompt. A consumer must re-resolve the entry and check current
// source/authority before reading files, modifying a candidate or publishing.
func (h *SkillHubPublicationHumanCommitHandler) writeRepairContext(ctx context.Context, w http.ResponseWriter, actor int64, original store.SkillHubPublicationCommitContext, report PublicationValidationReport) {
	// Recheck even an ineligible report: a peer round trip must not disclose
	// whether repair is possible after the publisher loses current ownership.
	latest, err := h.reader.GetSkillHubPublicationCommitContext(ctx, actor, original.Entry.ID, true)
	if err != nil || ctx.Err() != nil || latest == nil || latest.Entry.ID != original.Entry.ID ||
		latest.Entry.Input != original.Entry.Input || latest.Entry.IdentityHash != original.Entry.IdentityHash ||
		latest.Entry.State != "waiting_runtime" || latest.Intent != nil || latest.Receipt == nil ||
		original.Receipt == nil || *latest.Receipt != *original.Receipt {
		skillHubPublicationReadError(w, 503, "publication.authority_changed")
		return
	}
	if report.Outcome != "rejected" {
		skillHubPublicationReadError(w, 409, "publication.rejection_required")
		return
	}
	writeSkillHubPublicationJSON(w, 200, map[string]any{
		"schemaVersion": 1, "entryRequestId": original.Entry.ID,
		"botUid":     strconv.FormatInt(original.Entry.Input.BotUID, 10),
		"entrypoint": "workspace", "contextKind": "publication_rejection",
		"validation": report,
		"constraints": map[string]any{
			"maxFormatRepairRounds": 3, "descriptionDraftRequiresConfirmation": true,
			"stopOnFunctionalIdentitySecurityChanges": true,
			"automaticRepairEnabled":                  false,
		},
	})
}
