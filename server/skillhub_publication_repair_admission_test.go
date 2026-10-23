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

type repairAdmissionDB struct {
	*repairContextDB
	task          *store.SkillHubPublicationRepairTask
	starts, reads int
	createErr     error
}

func (s *repairAdmissionDB) CreateSkillHubPublicationRepairTask(ctx context.Context, actor int64, in store.SkillHubPublicationRepairInput) (*store.SkillHubPublicationRepairTask, error) {
	s.starts++
	if s.createErr != nil {
		return nil, s.createErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if actor != 1 {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if s.task != nil {
		if s.task.Input != in {
			return nil, store.ErrSkillHubPublicationConflict
		}
		return s.task, nil
	}
	hash, err := in.IdentityHash()
	if err != nil {
		return nil, err
	}
	s.task = &store.SkillHubPublicationRepairTask{ID: "pubrepair_" + strings.Repeat("a", 32), Input: in, IdentityHash: hash, CreatedAt: time.Now()}
	return s.task, nil
}
func (s *repairAdmissionDB) GetSkillHubPublicationRepairTask(context.Context, int64, string) (*store.SkillHubPublicationRepairTask, error) {
	s.reads++
	return s.task, nil
}
func (*repairAdmissionDB) ClaimSkillHubPublicationRepairRound(context.Context, int64, string, store.SkillHubPublicationRepairRoundInput, string) (*store.SkillHubPublicationRepairRound, error) {
	panic("admission must not spend repair rounds")
}
func (*repairAdmissionDB) FinishSkillHubPublicationRepairRound(context.Context, int64, store.SkillHubPublicationRepairLease, store.SkillHubPublicationRepairResult) error {
	panic("admission must not execute repairs")
}
func repairAdmissionFixture(t *testing.T) (*repairAdmissionDB, *humanReportCloud, *SkillHubPublicationHumanCommitHandler) {
	t.Helper()
	s, cloud, _ := repairContextFixture(t)
	db := &repairAdmissionDB{repairContextDB: s}
	h := newPublicationHumanCommitHandler(db, cloud, true)
	h.repairEnabled = true
	return db, cloud, h
}
func repairAdmissionRequest(t *testing.T, uid int64) *http.Request {
	return humanCommitRequest(t, "repair-task", `{"schemaVersion":1,"topicId":"grp_123"}`, uid)
}

func TestSkillHubPublicationHumanCommitRepairAdmissionFeatureFlags(t *testing.T) {
	for _, flags := range []struct {
		human, repair string
		enabled       bool
	}{{"", "true", false}, {"true", "", false}, {"true", "true", true}} {
		t.Run(flags.human+"/"+flags.repair, func(t *testing.T) {
			db, _, _ := repairAdmissionFixture(t)
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED", flags.human)
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_ENABLED", flags.repair)
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_ISSUER", "https://cats.example")
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE", "https://hub.example")
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET", strings.Repeat("a", 64))
			h := NewSkillHubPublicationHumanCommitHandlerFromEnv(db)
			if (h.enabled && h.repairEnabled) != flags.enabled {
				t.Fatal("repair admission flag precedence")
			}
			if !flags.enabled {
				humanCommitRun(t, h, repairAdmissionRequest(t, 1), 404)
			}
			if db.starts != 0 {
				t.Fatal("feature construction admitted task")
			}
		})
	}
}

func TestSkillHubPublicationHumanCommitRepairAdmissionPrivateAndIdempotent(t *testing.T) {
	for _, tc := range []struct {
		uid    int64
		status int
	}{{0, 401}, {1, 200}, {2, 404}, {3, 403}, {4, 403}, {5, 403}, {6, 401}} {
		t.Run(string(rune('0'+tc.uid)), func(t *testing.T) {
			db, cloud, h := repairAdmissionFixture(t)
			calls := 0
			cloud.report = func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				calls++
				return publicationReportFixture(r), nil
			}
			w := humanCommitRun(t, h, repairAdmissionRequest(t, tc.uid), tc.status)
			if tc.status != 200 {
				if calls != 0 || db.starts != 0 || db.task != nil {
					t.Fatal("unauthorized admission reached Cloud or store")
				}
				return
			}
			var out struct {
				SchemaVersion  int    `json:"schemaVersion"`
				EntryRequestID string `json:"entryRequestId"`
				BotUID         string `json:"botUid"`
				TopicID        string `json:"topicId"`
				TaskID         string `json:"taskId"`
				State          string `json:"state"`
				Rounds         int    `json:"roundsUsed"`
				MaxRounds      int    `json:"maxFormatRepairRounds"`
				Automatic      bool   `json:"automaticRepairEnabled"`
			}
			dec := json.NewDecoder(strings.NewReader(w.Body.String()))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&out); err != nil || out.SchemaVersion != 1 || out.EntryRequestID != publicationReadTestID || out.BotUID != "99" || out.TopicID != "grp_123" || out.TaskID != db.task.ID || out.State != "queued" || out.Rounds != 0 || out.MaxRounds != 3 || out.Automatic {
				t.Fatalf("admission projection %+v %v", out, err)
			}
			for _, secret := range []string{"receipt", "lease", "ReportHash", "candidateHash", "validation", "pubgrant_", "pubtask_"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("internal evidence leaked", secret)
				}
			}
			want, _ := publicationRepairEvidence(publicationReportFixture(db.receipt).Validation, db.receipt.Query.SourceSnapshotHash)
			if db.task.Input.Receipt != db.receipt || db.task.Input.Evidence != want {
				t.Fatal("unverified evidence admitted")
			}
			first := db.task
			humanCommitRun(t, h, repairAdmissionRequest(t, 1), 200)
			if db.task != first || len(first.Rounds) != 0 || db.records != 0 {
				t.Fatal("replay reset task or admitted publication")
			}
			r := humanCommitRequest(t, "repair-task", "", 1)
			r.Method = "GET"
			humanCommitRun(t, h, r, 200)
			if calls != 2 || db.reads != 1 {
				t.Fatal("read triggered Cloud or failed to use ledger")
			}
		})
	}
}

func TestSkillHubPublicationHumanCommitRepairAdmissionStrictTransport(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing_topic", `{"schemaVersion":1}`, 400},
		{"client_evidence", `{"schemaVersion":1,"topicId":"grp_123","evidence":{}}`, 400},
		{"duplicate_topic", `{"schemaVersion":1,"topicId":"grp_123","topicId":"grp_456"}`, 400},
		{"null_topic", `{"schemaVersion":1,"topicId":null}`, 400},
		{"noncanonical_topic", `{"schemaVersion":1,"topicId":"grp_0123"}`, 400},
		{"overflow_topic", `{"schemaVersion":1,"topicId":"grp_9223372036854775808"}`, 400},
		{"bad_schema", `{"schemaVersion":2,"topicId":"grp_123"}`, 400},
		{"query", "", 400}, {"encoding", "", 400}, {"cookie_only", "", 401}, {"duplicate_auth", "", 401}, {"put", "", 405}, {"default_off", "", 404}, {"unsupported_store", "", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, cloud, h := repairAdmissionFixture(t)
			cloud.report = func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				t.Fatal("invalid transport reached peer")
				return nil, nil
			}
			r := repairAdmissionRequest(t, 1)
			if tc.body != "" {
				r = humanCommitRequest(t, "repair-task", tc.body, 1)
			}
			switch tc.name {
			case "query":
				r.URL.RawQuery = "topicId=grp_123"
			case "encoding":
				r.Header.Set("Content-Encoding", "gzip")
			case "cookie_only":
				r.Header.Del("Authorization")
				r.AddCookie(&http.Cookie{Name: "session", Value: "private-token"})
			case "duplicate_auth":
				r.Header.Add("Authorization", r.Header.Get("Authorization"))
			case "put":
				r.Method = "PUT"
			case "default_off":
				h.repairEnabled = false
			case "unsupported_store":
				h.repairs = nil
			}
			humanCommitRun(t, h, r, tc.status)
			if db.starts != 0 || len(db.currentReads) != 0 {
				t.Fatal("invalid request touched ledger")
			}
		})
	}
}

func TestSkillHubPublicationHumanCommitRepairAdmissionChangedEvidenceAndAuthority(t *testing.T) {
	for _, mode := range []string{"passed", "truncated", "outage", "wrong_receipt", "wrong_candidate", "transfer", "cancel", "intent", "source", "identity", "request_cancelled", "store_denied", "store_cancelled", "store_conflict"} {
		t.Run(mode, func(t *testing.T) {
			db, cloud, h := repairAdmissionFixture(t)
			r := repairAdmissionRequest(t, 1)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			cloud.report = func(_ context.Context, receipt store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				out := publicationReportFixture(receipt)
				switch mode {
				case "passed":
					out.Validation.Outcome = "passed"
					out.Validation.NextAction = "none"
					out.Validation.Issues = []PublicationValidationIssue{}
				case "truncated":
					out.Validation.Truncated = true
				case "outage":
					return nil, errors.New("private-peer-secret")
				case "wrong_receipt":
					out.Receipt.AcceptanceEpoch++
				case "wrong_candidate":
					out.Validation.CandidateHash = strings.Repeat("f", 64)
				case "transfer":
					db.denyCurrent = true
				case "cancel":
					db.entry.State = "cancelled"
				case "intent":
					db.intent = &store.SkillHubPublicationCommitIntent{}
				case "source":
					db.entry.Input.Source.LocalSkillID = "changed"
				case "identity":
					db.entry.IdentityHash = "changed"
				case "request_cancelled":
					cancel()
				case "store_denied":
					db.createErr = store.ErrSkillHubPublicationDenied
				case "store_cancelled":
					db.createErr = store.ErrSkillHubPublicationSettled
				case "store_conflict":
					db.createErr = store.ErrSkillHubPublicationConflict
				}
				return out, nil
			}
			want := 503
			switch mode {
			case "passed", "truncated", "store_cancelled", "store_conflict":
				want = 409
			case "store_denied":
				want = 403
			}
			w := humanCommitRun(t, h, r, want)
			if db.task != nil || db.records != 0 || strings.Contains(w.Body.String(), "private-") {
				t.Fatal("failure leaked or admitted task")
			}
			if !strings.HasPrefix(mode, "store_") && db.starts != 0 {
				t.Fatal("invalid report reached admission")
			}
		})
	}
}

func TestSkillHubPublicationHumanCommitRepairAdmissionLimitsAndRecovery(t *testing.T) {
	db, _, h := repairAdmissionFixture(t)
	r := humanCommitRequest(t, "repair-task", "", 1)
	r.Method = "GET"
	humanCommitRun(t, h, r, 404)
	humanCommitRun(t, h, repairAdmissionRequest(t, 1), 200)
	humanCommitRun(t, h, repairAdmissionRequest(t, 1), 200)
	humanCommitRun(t, h, repairAdmissionRequest(t, 1), 429)
	if db.starts != 2 {
		t.Fatal("rate limit reached store")
	}
	// A reconstructed handler recovers the same persisted task without a new start.
	h = newPublicationHumanCommitHandler(db, h.cloud, true)
	h.repairEnabled = true
	r = humanCommitRequest(t, "repair-task", "", 1)
	r.Method = "GET"
	humanCommitRun(t, h, r, 200)
	for i := 0; i < cap(h.slots); i++ {
		h.slots <- struct{}{}
	}
	humanCommitRun(t, h, repairAdmissionRequest(t, 1), 503)
	if db.starts != 2 {
		t.Fatal("saturated request admitted")
	}
}

func TestSkillHubPublicationRepairEvidenceNormalizedIdentity(t *testing.T) {
	r := publicationReportFixture(cancellationTestLease("https://hub.example").Receipt).Validation
	r.Issues = append(r.Issues, PublicationValidationIssue{Code: "metadata.name_missing", Category: "format", Blocking: true, Location: PublicationValidationLocation{RelativePath: "SKILL.md", Field: "name"}, Message: "name is missing", RepairPolicy: "manual"})
	base, err := publicationRepairEvidence(r, r.CandidateHash)
	if err != nil {
		t.Fatal(err)
	}
	other := r
	other.Issues = append([]PublicationValidationIssue(nil), r.Issues...)
	other.ReportID = "99999999-1234-4123-8123-123456789abc"
	other.Issues[0].Message = "different display text"
	other.Issues[0], other.Issues[1] = other.Issues[1], other.Issues[0]
	other.Issues = append(other.Issues, other.Issues[0])
	got, err := publicationRepairEvidence(other, r.CandidateHash)
	if err != nil || got.IssueSetHash != base.IssueSetHash || got.ReportHash == base.ReportHash {
		t.Fatal("fresh identity/prose/order changed progress", err)
	}
	for _, change := range []func(*PublicationValidationIssue){
		func(i *PublicationValidationIssue) { i.Code = "metadata.name_invalid" },
		func(i *PublicationValidationIssue) { i.Category = "compatibility" },
		func(i *PublicationValidationIssue) { i.Blocking = false },
		func(i *PublicationValidationIssue) { i.Location.RelativePath = "skill.json" },
		func(i *PublicationValidationIssue) { i.Location.Field = "id" },
		func(i *PublicationValidationIssue) { n := int64(1); i.Location.EntryIndex = &n },
		func(i *PublicationValidationIssue) { i.RepairPolicy = "none" },
	} {
		x := r
		x.Issues = append([]PublicationValidationIssue(nil), r.Issues...)
		change(&x.Issues[1])
		got, err := publicationRepairEvidence(x, r.CandidateHash)
		if err != nil || got.IssueSetHash == base.IssueSetHash {
			t.Fatal("issue identity change omitted", err)
		}
	}
}
