package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type descriptionReleaseAuthorizeTestStore struct {
	*descriptionContinuationTestStore
	intent      store.SkillHubPublicationDescriptionReleaseIntent
	intentCalls int
}

func (db *descriptionReleaseAuthorizeTestStore) GetSkillHubPublicationDescriptionReleaseIntent(_ context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseIntent, error) {
	db.intentCalls++
	if actor != db.intent.ActorUID || id != db.intent.EntryID {
		return nil, store.ErrSkillHubPublicationDenied
	}
	copy := db.intent
	return &copy, nil
}

func releaseAuthorizeWire(p publicationDescriptionCloudReleasePermit) map[string]any {
	return map[string]any{"schemaVersion": p.SchemaVersion, "applicationId": p.ApplicationID, "replayed": p.Replayed,
		"permit": map[string]any{"schemaVersion": p.SchemaVersion, "applicationId": p.ApplicationID,
			"entryId": p.EntryID, "target": p.Target, "intentHash": p.IntentHash,
			"permitId": p.PermitID, "authorizedAt": p.AuthorizedAt}}
}

func releaseAuthorizeFixture(t *testing.T) (*descriptionReleaseAuthorizeTestStore, *publicationDescriptionPlannedRelease,
	store.SkillHubPublicationDescriptionReleaseIntent, *publicationDescriptionCloudReleasePermit) {
	t.Helper()
	base, handoff := descriptionPlanHandoff(t)
	continuation := snapshotDescriptionContinuation(handoff.checkpoint)
	plan := descriptionPlanFixture(continuation.Application, false)
	planned := &publicationDescriptionPlannedRelease{checkpoint: continuation, plan: plan}
	intent := store.SkillHubPublicationDescriptionReleaseIntent{EntryID: continuation.Draft.EntryID, ActorUID: 1,
		ApplicationID: continuation.Application.ID, AcceptanceIdentityHash: continuation.Acceptance.IdentityHash,
		Target: plan.Target, RequestedAt: continuation.Acceptance.AcceptedAt.Add(time.Second)}
	intent.IdentityHash, _ = intent.Digest()
	db := &descriptionReleaseAuthorizeTestStore{descriptionContinuationTestStore: base, intent: intent}
	proof := publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: intent.ApplicationID,
		EntryID: intent.EntryID, Target: intent.Target, IntentHash: intent.IdentityHash,
		PermitID:     "pubdescpermit_" + strings.Repeat("a", 32),
		AuthorizedAt: intent.RequestedAt.Add(time.Second).Format(time.RFC3339Nano)}
	replayed := false
	proof.Replayed = &replayed
	cloud := &proof
	return db, planned, intent, cloud
}

func TestAuthorizeConfirmedPublicationDescriptionRelease(t *testing.T) {
	db, handoff, intent, proof := releaseAuthorizeFixture(t)
	verifies, calls := 0, 0
	ports := publicationDescriptionReleaseAuthorizePorts{
		VerifySource: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication) error {
			verifies++
			if app.ID != intent.ApplicationID {
				t.Fatal("source identity changed")
			}
			return nil
		},
		AuthorizeCloud: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication,
			got store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error) {
			calls++
			if app.ID != intent.ApplicationID || got != intent {
				t.Fatal("cloud request was not bound to the exact human intent")
			}
			return proof, nil
		},
	}
	got, err := authorizeConfirmedPublicationDescriptionRelease(context.Background(), db, 1, intent.EntryID, handoff, ports)
	if err != nil || got == nil || !reflect.DeepEqual(got.permit, *proof) || got.plan != handoff.plan ||
		verifies != 2 || calls != 1 || db.intentCalls != 4 {
		t.Fatalf("authorization did not preserve current checkpoint/intent: err=%v got=%+v want=%+v verifies=%d calls=%d intentReads=%d planEqual=%v",
			err, got, proof, verifies, calls, db.intentCalls, got != nil && got.plan == handoff.plan)
	}
	if db.task.State() != "waiting_confirmation" || len(db.task.Rounds) != 1 || db.completes != 1 || db.writes != 1 {
		t.Fatal("authorization changed task history or repair budget")
	}
}

func TestAuthorizeConfirmedPublicationDescriptionReleaseFences(t *testing.T) {
	for _, mode := range []string{"missing-intent", "changed-intent", "changed-checkpoint", "source-before", "source-after", "cloud-denied", "bad-proof", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			db, handoff, intent, proof := releaseAuthorizeFixture(t)
			verifies, calls := 0, 0
			if mode == "missing-intent" {
				db.intent.ApplicationID += "x"
			}
			ports := publicationDescriptionReleaseAuthorizePorts{
				VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
					verifies++
					if mode == "source-before" && verifies == 1 || mode == "source-after" && verifies == 2 {
						return store.ErrSkillHubPublicationConflict
					}
					return nil
				},
				AuthorizeCloud: func(_ context.Context, _ store.SkillHubPublicationDescriptionApplication,
					_ store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error) {
					calls++
					switch mode {
					case "changed-intent":
						db.intent.Target.Version = "9.9.9"
					case "changed-checkpoint":
						db.task.Rounds[0].Lease.WorkerID += "-changed"
					case "bad-proof":
						altered := *proof
						altered.IntentHash = strings.Repeat("f", 64)
						return &altered, nil
					case "cancel":
						db.task.Rounds[0].Result.Outcome = "cancelled"
					case "cloud-denied":
						return nil, store.ErrSkillHubPublicationDenied
					}
					return proof, nil
				},
			}
			got, err := authorizeConfirmedPublicationDescriptionRelease(context.Background(), db, 1, intent.EntryID, handoff, ports)
			if err == nil || got != nil {
				t.Fatal("unfenced release permit accepted", mode)
			}
			if (mode == "missing-intent" || mode == "source-before") && calls != 0 {
				t.Fatal("invalid precondition reached Cloud", mode)
			}
		})
	}
}

func releaseAuthorizeClientFixture(t *testing.T, origin string) (store.SkillHubPublicationDescriptionApplication,
	store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) {
	t.Helper()
	app := completedDescriptionApplication(t, origin, []byte("derived-copy"))
	target := commitClientTarget(app.Input.Receipt)
	target.CandidateHash = app.Result.Derivation.DerivedCandidateHash
	intent := store.SkillHubPublicationDescriptionReleaseIntent{EntryID: app.Input.Receipt.Query.EntryID, ActorUID: 1,
		ApplicationID: app.ID, AcceptanceIdentityHash: strings.Repeat("d", 64), Target: target,
		RequestedAt: time.Date(2026, 10, 26, 0, 0, 1, 0, time.UTC)}
	intent.IdentityHash, _ = intent.Digest()
	wire := publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: app.ID, EntryID: intent.EntryID,
		Target: intent.Target, IntentHash: intent.IdentityHash, PermitID: "pubdescpermit_" + strings.Repeat("e", 32),
		AuthorizedAt: intent.RequestedAt.Add(time.Second).Format(time.RFC3339Nano)}
	replayed := false
	wire.Replayed = &replayed
	return app, intent, wire
}

func TestPublicationDescriptionReleaseAuthorizeClientHTTPS(t *testing.T) {
	var app store.SkillHubPublicationDescriptionApplication
	var intent store.SkillHubPublicationDescriptionReleaseIntent
	var wire publicationDescriptionCloudReleasePermit
	var calls int
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != publicationDescriptionReleaseAuthorizePath ||
			r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("invalid service request")
		}
		raw, _ := io.ReadAll(r.Body)
		request, err := publicationExactObject(raw, "schemaVersion", "applicationId", "publicationReceipt")
		if err != nil || string(request["applicationId"]) != `"`+app.ID+`"` {
			t.Error("request is not bound to the retained application", err)
		}
		if _, err := publicationExactObject(request["publicationReceipt"], "schemaVersion", "entryRequestId",
			"candidateRevision", "acceptanceEpoch", "requestHash", "sourceSnapshotHash", "requestBytes",
			"delegationId", "idempotencyKey", "taskId", "eventId"); err != nil {
			t.Error("original receipt shape changed", err)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(releaseAuthorizeWire(wire))
	}))
	defer s.Close()
	app, intent, wire = releaseAuthorizeClientFixture(t, s.URL)
	cfg := derivationClientConfig(s)
	cfg.PlanEnabled, cfg.ReleaseAuthorizeEnabled = true, true
	c, err := NewPublicationDescriptionDerivationClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.AuthorizeRetainedRelease(context.Background(), app, intent)
	if err != nil || got == nil || !reflect.DeepEqual(*got, wire) || calls != 1 {
		t.Fatalf("bad permit response: err=%v got=%+v want=%+v calls=%d", err, got, wire, calls)
	}
}

func TestPublicationDescriptionReleaseAuthorizeClientRejectsUnsafeResponses(t *testing.T) {
	for _, mode := range []string{"duplicate", "nested-duplicate", "unknown", "null-replay", "null-target", "null-policy", "wrong-application", "wrong-intent", "wrong-target", "early-time", "offset-time", "cacheable", "oversize", "encoding", "redirect", "denied", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var app store.SkillHubPublicationDescriptionApplication
			var intent store.SkillHubPublicationDescriptionReleaseIntent
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				permit := publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: app.ID,
					EntryID: intent.EntryID, Target: intent.Target, IntentHash: intent.IdentityHash,
					PermitID:     "pubdescpermit_" + strings.Repeat("e", 32),
					AuthorizedAt: intent.RequestedAt.Add(time.Second).Format(time.RFC3339Nano)}
				if mode == "wrong-intent" {
					permit.IntentHash = strings.Repeat("f", 64)
				}
				if mode == "wrong-target" {
					permit.Target.Version = "9.9.9"
				}
				if mode == "early-time" {
					permit.AuthorizedAt = intent.RequestedAt.Add(-time.Second).Format(time.RFC3339Nano)
				}
				if mode == "offset-time" {
					permit.AuthorizedAt = intent.RequestedAt.Add(time.Second).Format("2006-01-02T15:04:05-07:00")
				}
				if mode == "redirect" {
					w.Header().Set("Location", "https://attacker.invalid/")
					w.WriteHeader(http.StatusFound)
					return
				}
				if mode == "denied" || mode == "unavailable" {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.Header().Set("Cache-Control", "no-store")
					status := http.StatusForbidden
					if mode == "unavailable" {
						status = http.StatusServiceUnavailable
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"code":"skillhub.publication_description_derivation_denied","message":"denied"}}`))
					return
				}
				replayed := false
				permit.Replayed = &replayed
				out := releaseAuthorizeWire(permit)
				switch mode {
				case "null-replay":
					out["replayed"] = nil
				case "null-target":
					out["permit"].(map[string]any)["target"] = nil
				case "unknown":
					out["permit"].(map[string]any)["privatePath"] = "hidden"
				case "wrong-application":
					out["applicationId"] = "pubdesc_" + strings.Repeat("a", 64)
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				raw, _ := json.Marshal(out)
				switch mode {
				case "nested-duplicate":
					raw = []byte(strings.Replace(string(raw), `"intentHash":`, `"intentHash":"`+intent.IdentityHash+`","intentHash":`, 1))
				case "null-policy":
					raw = []byte(strings.Replace(string(raw), `"confirmVersionPublish":false`, `"confirmVersionPublish":null`, 1))
				case "cacheable":
					w.Header().Set("Cache-Control", "public")
				case "oversize":
					raw = append(raw, []byte(strings.Repeat(" ", 8193))...)
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				}
				if mode == "duplicate" {
					raw = append(raw[:len(raw)-1], []byte(`,"replayed":false}`)...)
				}
				_, _ = w.Write(raw)
			}))
			defer s.Close()
			app, intent, _ = releaseAuthorizeClientFixture(t, s.URL)
			cfg := derivationClientConfig(s)
			cfg.PlanEnabled, cfg.ReleaseAuthorizeEnabled = true, true
			c, err := NewPublicationDescriptionDerivationClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.AuthorizeRetainedRelease(context.Background(), app, intent)
			if got != nil || err == nil {
				t.Fatal("unsafe release response accepted", mode, err)
			}
			if mode == "denied" && !errors.Is(err, store.ErrSkillHubPublicationDenied) {
				t.Fatal("denial did not map to a fixed CatsCo error", err)
			}
			if mode != "denied" && !errors.Is(err, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer) {
				t.Fatal("ambiguous response was treated as a definitive denial", err)
			}
		})
	}
}
