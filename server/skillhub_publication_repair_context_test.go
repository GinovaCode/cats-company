package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type repairContextDB struct {
	*humanCommitDB
	currentReads []bool
	denyCurrent  bool
}

func (s *repairContextDB) GetSkillHubPublicationCommitContext(ctx context.Context, actor int64, id string, current bool) (*store.SkillHubPublicationCommitContext, error) {
	s.currentReads = append(s.currentReads, current)
	if current && s.denyCurrent {
		return nil, store.ErrSkillHubPublicationDenied
	}
	return s.humanCommitDB.GetSkillHubPublicationCommitContext(ctx, actor, id, current)
}

func repairContextFixture(t *testing.T) (*repairContextDB, *humanReportCloud, *SkillHubPublicationHumanCommitHandler) {
	t.Helper()
	db, cloud, _ := humanCommitFixture()
	c := &humanReportCloud{cloud, func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		return publicationReportFixture(r), nil
	}}
	cloud.plan = func(context.Context) error { t.Fatal("repair context planned a publication"); return nil }
	cloud.authorize = func(context.Context) error { t.Fatal("repair context authorized a publication"); return nil }
	cloud.read = func(context.Context) error { t.Fatal("repair context read execution state"); return nil }
	s := &repairContextDB{humanCommitDB: db}
	return s, c, newPublicationHumanCommitHandler(s, c, true)
}

func repairContextRequest(t *testing.T, uid int64) *http.Request {
	r := humanCommitRequest(t, "repair-context", "", uid)
	r.Method = http.MethodGet
	return r
}

func TestSkillHubPublicationHumanCommitRepairContextPrivateReadOnly(t *testing.T) {
	for _, tc := range []struct {
		uid    int64
		status int
	}{{0, 401}, {1, 200}, {2, 404}, {3, 403}, {4, 403}, {5, 403}, {6, 401}} {
		t.Run(string(rune('0'+tc.uid)), func(t *testing.T) {
			db, c, h := repairContextFixture(t)
			calls := 0
			c.report = func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				calls++
				return publicationReportFixture(r), nil
			}
			w := humanCommitRun(t, h, repairContextRequest(t, tc.uid), tc.status)
			if tc.status == 200 {
				var out struct {
					SchemaVersion  int                         `json:"schemaVersion"`
					EntryRequestID string                      `json:"entryRequestId"`
					BotUID         string                      `json:"botUid"`
					Entrypoint     string                      `json:"entrypoint"`
					ContextKind    string                      `json:"contextKind"`
					Validation     PublicationValidationReport `json:"validation"`
					Constraints    struct {
						MaxRounds          int  `json:"maxFormatRepairRounds"`
						ConfirmDescription bool `json:"descriptionDraftRequiresConfirmation"`
						StopSemantic       bool `json:"stopOnFunctionalIdentitySecurityChanges"`
						Automatic          bool `json:"automaticRepairEnabled"`
					} `json:"constraints"`
				}
				dec := json.NewDecoder(strings.NewReader(w.Body.String()))
				dec.DisallowUnknownFields()
				if dec.Decode(&out) != nil || out.SchemaVersion != 1 || out.EntryRequestID != publicationReadTestID || out.BotUID != "99" ||
					out.Entrypoint != "workspace" || out.ContextKind != "publication_rejection" || calls != 1 {
					t.Fatal("wrong context", w.Body.String())
				}
				if out.Constraints.MaxRounds != 3 || !out.Constraints.ConfirmDescription || !out.Constraints.StopSemantic || out.Constraints.Automatic ||
					out.Validation.CandidateHash != db.receipt.Query.SourceSnapshotHash || out.Validation.Issues[0].RepairPolicy != "confirmation_required" {
					t.Fatal("lost report binding or repair boundary", w.Body.String())
				}
				if len(db.currentReads) != 2 || !db.currentReads[0] || !db.currentReads[1] {
					t.Fatal("must recheck current authority before and after peer IO")
				}
				for _, secret := range []string{"private-", "receipt", "pubtask_", "pubgrant_", "pubcandidate_", "requestHash", "publisherUserId"} {
					if strings.Contains(w.Body.String(), secret) {
						t.Fatal("context leaked internal data", secret)
					}
				}
			} else if calls != 0 {
				t.Fatal("unauthorized peer read")
			}
			if db.records != 0 || db.intent != nil {
				t.Fatal("context read created intent")
			}
		})
	}
}

func TestSkillHubPublicationHumanCommitRepairContextAuthorityAndEvidence(t *testing.T) {
	for _, mode := range []string{"transferred", "cancelled", "existing_intent", "wrong_entrypoint", "passed", "outage", "wrong_receipt", "wrong_candidate",
		"transferred_during_read", "cancelled_during_read", "intent_during_read", "source_during_read", "receipt_during_read", "identity_during_read", "request_cancelled"} {
		t.Run(mode, func(t *testing.T) {
			db, c, h := repairContextFixture(t)
			r := repairContextRequest(t, 1)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			calls := 0
			c.report = func(_ context.Context, receipt store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				calls++
				out := publicationReportFixture(receipt)
				switch mode {
				case "passed":
					out.Validation.Outcome, out.Validation.NextAction, out.Validation.Issues = "passed", "none", []PublicationValidationIssue{}
				case "outage":
					return nil, errors.New("private-upstream-token")
				case "wrong_receipt":
					out.Receipt.AcceptanceEpoch++
				case "wrong_candidate":
					out.Validation.CandidateHash = strings.Repeat("f", 64)
				case "transferred_during_read":
					db.denyCurrent = true
				case "cancelled_during_read":
					db.entry.State = "cancelled"
				case "intent_during_read":
					db.intent = &store.SkillHubPublicationCommitIntent{}
				case "source_during_read":
					db.entry.Input.Source.LocalSkillID = "changed-source"
				case "receipt_during_read":
					db.receipt.EventID = "pubevent_99999999-1234-4123-8123-123456789abc"
				case "identity_during_read":
					db.entry.IdentityHash = "changed-identity"
				case "request_cancelled":
					cancel()
				}
				return out, nil
			}
			want := 503
			switch mode {
			case "transferred":
				db.denyCurrent = true
				want = 403
			case "cancelled":
				db.entry.State = "cancelled"
				want = 409
			case "existing_intent":
				db.intent = &store.SkillHubPublicationCommitIntent{}
				want = 409
			case "wrong_entrypoint":
				db.entry.Input.Entrypoint = "chat"
				want = 409
			case "passed":
				want = 409
			}
			w := humanCommitRun(t, h, r, want)
			if db.records != 0 || strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "\"validation\"") {
				t.Fatal("error leaked context or created intent", w.Body.String())
			}
			if (mode == "transferred" || mode == "cancelled" || mode == "existing_intent" || mode == "wrong_entrypoint") && calls != 0 {
				t.Fatal("ineligible context reached peer")
			}
		})
	}
	// An original active publisher can still read history after transfer;
	// that historical access must never become repair authority.
	db, _, h := repairContextFixture(t)
	db.denyCurrent = true
	humanCommitRun(t, h, humanReportRequest(t, 1), 200)
	humanCommitRun(t, h, repairContextRequest(t, 1), 403)
}

func TestSkillHubPublicationHumanCommitRepairContextStrictTransport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		mutate func(*http.Request, *SkillHubPublicationHumanCommitHandler)
	}{
		{"post", 405, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) { r.Method = "POST" }},
		{"body", 400, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) {
			r.Body = io.NopCloser(strings.NewReader("{}"))
			r.ContentLength = 2
		}},
		{"query", 400, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) { r.URL.RawQuery = "botUid=100" }},
		{"encoded_path", 404, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) { r.URL.RawPath = r.URL.Path }},
		{"cookie_only", 401, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) {
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: "session", Value: "secret"})
		}},
		{"duplicate_auth", 401, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) {
			r.Header.Add("Authorization", r.Header.Get("Authorization"))
		}},
		{"encoding", 400, func(r *http.Request, _ *SkillHubPublicationHumanCommitHandler) {
			r.Header.Set("Content-Encoding", "gzip")
		}},
		{"disabled", 404, func(_ *http.Request, h *SkillHubPublicationHumanCommitHandler) { h.enabled = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, c, h := repairContextFixture(t)
			c.report = func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				t.Fatal("invalid transport reached Cloud")
				return nil, nil
			}
			r := repairContextRequest(t, 1)
			tc.mutate(r, h)
			humanCommitRun(t, h, r, tc.status)
			if len(db.currentReads) != 0 {
				t.Fatal("invalid request reached store")
			}
		})
	}
}

func TestSkillHubPublicationHumanCommitRepairContextLimits(t *testing.T) {
	_, _, h := repairContextFixture(t)
	for i := 0; i < 3; i++ {
		humanCommitRun(t, h, repairContextRequest(t, 1), 200)
	}
	w := humanCommitRun(t, h, repairContextRequest(t, 1), 429)
	if w.Header().Get("Retry-After") != "5" {
		t.Fatal("missing retry budget")
	}
	db, c, h := repairContextFixture(t)
	c.report = func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		t.Fatal("saturated route called peer")
		return nil, nil
	}
	for i := 0; i < cap(h.slots); i++ {
		h.slots <- struct{}{}
	}
	humanCommitRun(t, h, repairContextRequest(t, 1), 503)
	if len(db.currentReads) != 0 {
		t.Fatal("saturated route reached store")
	}
}
