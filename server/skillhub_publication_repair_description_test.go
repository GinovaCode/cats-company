package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type descriptionTestDB struct {
	*repairAdmissionDB
	draft                           *store.SkillHubPublicationDescriptionDraft
	proposals, confirmations, reads int
	err                             error
	after                           func()
}

func (s *descriptionTestDB) ProposeSkillHubPublicationDescription(context.Context, int64, store.SkillHubPublicationRepairLease, store.SkillHubPublicationDescriptionProposal) (*store.SkillHubPublicationDescriptionDraft, error) {
	s.proposals++
	return s.draft, s.err
}
func (s *descriptionTestDB) GetSkillHubPublicationDescription(context.Context, int64, string) (*store.SkillHubPublicationDescriptionDraft, error) {
	s.reads++
	if s.after != nil {
		s.after()
	}
	return s.draft, s.err
}
func (s *descriptionTestDB) ConfirmSkillHubPublicationDescription(_ context.Context, actor int64, id, key string) (*store.SkillHubPublicationDescriptionDraft, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.draft == nil || actor != 1 || id != s.draft.EntryID || key != s.draft.ConfirmationKey {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if s.draft.ConfirmedAt == nil {
		s.confirmations++
		now := time.Now()
		s.draft.ConfirmedAt = &now
	}
	if s.after != nil {
		s.after()
	}
	return s.draft, nil
}
func descriptionFixture(t *testing.T) (*descriptionTestDB, *humanReportCloud, *SkillHubPublicationHumanCommitHandler) {
	t.Helper()
	s, cloud, _ := repairAdmissionFixture(t)
	evidence, err := publicationRepairEvidence(publicationReportFixture(s.receipt).Validation, s.receipt.Query.SourceSnapshotHash)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateSkillHubPublicationRepairTask(context.Background(), 1, store.SkillHubPublicationRepairInput{Receipt: s.receipt, TopicID: "grp_123", Evidence: evidence})
	if err != nil {
		t.Fatal(err)
	}
	lease := store.SkillHubPublicationRepairLease{EntryID: task.Input.Receipt.Query.EntryID, TaskID: task.ID, Round: 1,
		Input: store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: evidence}, WorkerID: "worker-1", Token: strings.Repeat("a", 32), Generation: 1, Until: time.Now().Add(time.Minute)}
	task.Rounds = []store.SkillHubPublicationRepairRound{{Lease: lease, Result: &store.SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: evidence}}}
	index := int64(0)
	roundHash, _ := lease.Input.IdentityHash()
	in := store.SkillHubPublicationDescriptionDraftInput{TaskID: task.ID, TaskIdentityHash: task.IdentityHash, Round: 1, RoundIdentityHash: roundHash, Evidence: evidence,
		Proposal: store.SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &index, Text: "Read a document and return its contents."}}
	key, _ := in.IdentityHash()
	db := &descriptionTestDB{repairAdmissionDB: s, draft: &store.SkillHubPublicationDescriptionDraft{EntryID: lease.EntryID, Input: in, ConfirmationKey: key, CreatedAt: time.Now()}}
	cloud.report = func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		t.Fatal("draft route called Cloud")
		return nil, nil
	}
	h := newPublicationHumanCommitHandler(db, cloud, true)
	h.repairEnabled = true
	return db, cloud, h
}
func descriptionRequest(t *testing.T, method, body string, uid int64) *http.Request {
	r := humanCommitRequest(t, "repair-description", body, uid)
	r.Method = method
	return r
}
func TestSkillHubPublicationHumanCommitDescriptionPrivateAndExact(t *testing.T) {
	for _, tc := range []struct {
		uid    int64
		status int
	}{{0, 401}, {1, 200}, {2, 404}, {3, 403}, {4, 403}, {5, 403}, {6, 401}} {
		t.Run(string(rune('0'+tc.uid)), func(t *testing.T) {
			db, _, h := descriptionFixture(t)
			w := humanCommitRun(t, h, descriptionRequest(t, "GET", "", tc.uid), tc.status)
			if tc.status != 200 {
				if db.reads != 0 || db.confirmations != 0 {
					t.Fatal("unauthorized access reached draft")
				}
				return
			}
			var out struct {
				Schema    int    `json:"schemaVersion"`
				Entry     string `json:"entryRequestId"`
				Bot       string `json:"botUid"`
				Topic     string `json:"topicId"`
				Task      string `json:"taskId"`
				Automatic bool   `json:"automaticRepairEnabled"`
				Draft     struct {
					Key      string                        `json:"confirmationKey"`
					Round    int                           `json:"round"`
					Location PublicationValidationLocation `json:"location"`
					Text     string                        `json:"text"`
					State    string                        `json:"state"`
				} `json:"draft"`
			}
			dec := json.NewDecoder(strings.NewReader(w.Body.String()))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&out); err != nil || out.Draft.Key != db.draft.ConfirmationKey || out.Draft.Text != db.draft.Input.Proposal.Text || out.Draft.State != "awaiting_confirmation" || out.Automatic || out.Bot != "99" {
				t.Fatal("draft projection", err, w.Body.String())
			}
			for _, secret := range []string{"candidateHash", "reportHash", "issueSetHash", "lease", "worker", "receipt", "taskIdentityHash", "roundIdentityHash"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("internal draft data leaked", secret)
				}
			}
			humanCommitRun(t, h, descriptionRequest(t, "POST", `{"schemaVersion":1,"confirmationKey":"`+strings.Repeat("f", 64)+`"}`, 1), 409)
			body := `{"schemaVersion":1,"confirmationKey":"` + out.Draft.Key + `"}`
			first := humanCommitRun(t, h, descriptionRequest(t, "POST", body, 1), 200)
			h = newPublicationHumanCommitHandler(db, h.cloud, true)
			h.repairEnabled = true
			second := humanCommitRun(t, h, descriptionRequest(t, "POST", body, 1), 200)
			if first.Body.String() != second.Body.String() || db.confirmations != 1 || !strings.Contains(second.Body.String(), `"state":"confirmed"`) || len(db.task.Rounds) != 1 || db.task.State() != "waiting_confirmation" || db.records != 0 || db.proposals != 0 {
				t.Fatal("confirmation replay ran or reset work")
			}
		})
	}
}
func TestSkillHubPublicationHumanCommitDescriptionStrictTransport(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		change     func(*http.Request, *SkillHubPublicationHumanCommitHandler)
		status     int
	}{
		{"missing_key", `{"schemaVersion":1}`, nil, 400},
		{"boolean_approval", `{"schemaVersion":1,"confirmed":true}`, nil, 400},
		{"null", `{"schemaVersion":1,"confirmationKey":null}`, nil, 400},
		{"duplicate", `{"schemaVersion":1,"confirmationKey":"KEY","confirmationKey":"KEY"}`, nil, 400},
		{"extra_text", `{"schemaVersion":1,"confirmationKey":"KEY","text":"replace"}`, nil, 400},
		{"candidate", `{"schemaVersion":1,"confirmationKey":"KEY","candidateHash":"KEY"}`, nil, 400},
		{"bad_key", `{"schemaVersion":1,"confirmationKey":"` + strings.Repeat("Z", 64) + `"}`, nil, 400},
		{"query", `{"schemaVersion":1,"confirmationKey":"KEY"}`, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) { r.URL.RawQuery = "force=1" }, 400},
		{"encoded", `{"schemaVersion":1,"confirmationKey":"KEY"}`, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) {
			r.Header.Set("Content-Encoding", "gzip")
		}, 400},
		{"cookie_only", `{"schemaVersion":1,"confirmationKey":"KEY"}`, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) {
			r.Header.Del("Authorization")
			r.Header.Set("Cookie", "session=1")
		}, 401},
		{"human_off", `{"schemaVersion":1,"confirmationKey":"KEY"}`, func(_ *http.Request, h *SkillHubPublicationHumanCommitHandler) { h.enabled = false }, 404},
		{"repair_off", `{"schemaVersion":1,"confirmationKey":"KEY"}`, func(_ *http.Request, h *SkillHubPublicationHumanCommitHandler) { h.repairEnabled = false }, 404},
		{"unsupported", `{"schemaVersion":1,"confirmationKey":"KEY"}`, func(_ *http.Request, h *SkillHubPublicationHumanCommitHandler) { h.descriptions = nil }, 503},
		{"put", `{"schemaVersion":1,"confirmationKey":"KEY"}`, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) { r.Method = "PUT" }, 405},
		{"get_body", `{"schemaVersion":1,"confirmationKey":"KEY"}`, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) { r.Method = "GET" }, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, h := descriptionFixture(t)
			body := strings.ReplaceAll(tc.body, "KEY", db.draft.ConfirmationKey)
			r := descriptionRequest(t, "POST", body, 1)
			if tc.change != nil {
				tc.change(r, h)
			}
			humanCommitRun(t, h, r, tc.status)
			if db.confirmations != 0 || db.reads != 0 || db.proposals != 0 {
				t.Fatal("invalid transport accessed draft")
			}
		})
	}
}
func TestSkillHubPublicationHumanCommitDescriptionChangedAuthorityAndEvidence(t *testing.T) {
	for _, change := range []string{"after_read", "after_confirmation", "bad_text", "bad_candidate", "bad_task", "missing", "store_denied", "store_uncertain"} {
		t.Run(change, func(t *testing.T) {
			db, _, h := descriptionFixture(t)
			status, method, body := 503, "GET", ""
			switch change {
			case "after_read":
				db.after = func() { db.denied = true }
			case "after_confirmation":
				db.after = func() { db.entry.Input.Source.RouteGeneration++ }
				method = "POST"
				body = `{"schemaVersion":1,"confirmationKey":"` + db.draft.ConfirmationKey + `"}`
			case "bad_text":
				db.draft.Input.Proposal.Text += "changed"
			case "bad_candidate":
				db.draft.Input.Evidence.CandidateHash = strings.Repeat("f", 64)
			case "bad_task":
				db.draft.Input.TaskID = "pubrepair_" + strings.Repeat("b", 32)
			case "missing":
				db.draft = nil
				status = 404
			case "store_denied":
				db.err = store.ErrSkillHubPublicationDenied
				status = 403
			case "store_uncertain":
				db.err = errors.New("private DB failure")
			}
			w := humanCommitRun(t, h, descriptionRequest(t, method, body, 1), status)
			if strings.Contains(w.Body.String(), "private DB failure") || strings.Contains(w.Body.String(), "Read a document") {
				t.Fatal("failed read leaked draft or raw error")
			}
		})
	}
}
func TestSkillHubPublicationDescriptionProposalRequiresBoundReport(t *testing.T) {
	for _, change := range []string{"valid", "changed_report", "passed", "truncated", "identity", "safety", "wrong_selector", "no_description"} {
		t.Run(change, func(t *testing.T) {
			db, _, _ := descriptionFixture(t)
			report := publicationReportFixture(db.receipt).Validation
			lease := db.task.Rounds[0].Lease
			p := db.draft.Input.Proposal
			switch change {
			case "changed_report":
				report.ReportID = "22345678-1234-4123-8123-123456789abc"
			case "passed":
				report.Outcome = "passed"
				report.NextAction = "none"
				report.Issues = []PublicationValidationIssue{}
			case "truncated":
				report.Truncated = true
			case "identity":
				report.Issues = append(report.Issues, PublicationValidationIssue{Code: "metadata.name_missing", Category: "format", Blocking: true, RepairPolicy: "manual", Message: "Needs a name"})
				lease.Input.Evidence, _ = publicationRepairEvidence(report, report.CandidateHash)
			case "safety":
				report.Issues = append(report.Issues, PublicationValidationIssue{Code: "package.unsafe_path", Category: "package_safety", Blocking: true, RepairPolicy: "manual", Message: "Unsafe package"})
				lease.Input.Evidence, _ = publicationRepairEvidence(report, report.CandidateHash)
			case "wrong_selector":
				n := int64(1)
				p.EntryIndex = &n
			case "no_description":
				report.Issues[0].Code = "metadata.name_missing"
				report.Issues[0].RepairPolicy = "manual"
				lease.Input.Evidence, _ = publicationRepairEvidence(report, report.CandidateHash)
			}
			_, err := proposePublicationDescription(context.Background(), db, 1, lease, report, p)
			if change == "valid" {
				if err != nil || db.proposals != 1 {
					t.Fatal("valid bound draft rejected", err)
				}
			} else if err == nil || db.proposals != 0 {
				t.Fatal("unverified or unsafe draft reached store", err)
			}
		})
	}
}
