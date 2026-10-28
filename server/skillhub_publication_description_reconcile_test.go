package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type descriptionReconcileTestStore struct {
	*terminalOutcomeTestStore
	lease     store.SkillHubPublicationDescriptionReconcileLease
	claims    atomic.Int32
	outcomes  []string
	claim     func(context.Context) (*store.SkillHubPublicationDescriptionReconcileLease, error)
	finishErr error
}

func (db *descriptionReconcileTestStore) ClaimSkillHubPublicationDescriptionReconcile(ctx context.Context, _ store.SkillHubPublicationCancellationScope) (*store.SkillHubPublicationDescriptionReconcileLease, error) {
	db.claims.Add(1)
	if db.claim != nil {
		return db.claim(ctx)
	}
	l := db.lease
	return &l, nil
}
func (db *descriptionReconcileTestStore) FinishSkillHubPublicationDescriptionReconcile(_ context.Context, l store.SkillHubPublicationDescriptionReconcileLease, outcome string) error {
	if l != db.lease {
		return store.ErrSkillHubPublicationStale
	}
	db.outcomes = append(db.outcomes, outcome)
	return db.finishErr
}
func descriptionReconcileFixture(t *testing.T, state string) (*PublicationDescriptionReconciler, *descriptionReconcileTestStore, *atomic.Int32) {
	t.Helper()
	r, s := descriptionOutcomeFixture(t)
	s.State = state
	if state == "published" {
		s.Result = &PublicationCommitCloudResult{SkillID: s.Target.SkillID, Version: s.Target.Version, PublishedAt: s.Target.ReleaseAt, PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64)}
	}
	if state == "publish_failed" {
		code := "skillhub.publication_description_release_commit_rejected"
		s.ErrorCode = &code
	}
	db := &descriptionReconcileTestStore{terminalOutcomeTestStore: &terminalOutcomeTestStore{descriptionOutcomeTestReader: r}, lease: store.SkillHubPublicationDescriptionReconcileLease{
		ActorUID: r.observation.Intent.ActorUID, EntryID: r.observation.Intent.EntryID, ApplicationID: r.observation.Intent.ApplicationID, Token: strings.Repeat("a", 32), Generation: 1, ExpiresAt: time.Now().Add(90 * time.Second)}}
	calls := &atomic.Int32{}
	w := &PublicationDescriptionReconciler{enabled: true, scope: store.SkillHubPublicationCancellationScope{Issuer: r.observation.Continuation.Application.Input.Receipt.Query.Issuer, Audience: r.observation.Continuation.Application.Input.Receipt.Query.Audience}, ledger: db,
		ports: publicationDescriptionReleaseOutcomePorts{ReadCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
			calls.Add(1)
			return &s, nil
		}}}
	return w, db, calls
}

func TestSkillHubPublicationDescriptionReconcileOutcomes(t *testing.T) {
	for _, state := range []string{"queued", "publishing", "committing", "published", "publish_failed"} {
		t.Run(state, func(t *testing.T) {
			w, db, calls := descriptionReconcileFixture(t, state)
			want := "progress"
			if state == "published" || state == "publish_failed" {
				want = "recorded"
			}
			if err := w.RunOnce(context.Background()); err != nil || len(db.outcomes) != 1 || db.outcomes[0] != want || calls.Load() != 1 {
				t.Fatal("outcome", db.outcomes, err)
			}
			if want == "progress" && db.writes != 0 {
				t.Fatal("progress wrote terminal")
			}
		})
	}
}

func TestSkillHubPublicationDescriptionReconcileLostResponseRecovery(t *testing.T) {
	w, db, calls := descriptionReconcileFixture(t, "published")
	db.lose = true
	if err := w.RunOnce(context.Background()); err != nil || db.outcomes[0] != "retry" || db.receipt == nil {
		t.Fatal("lost write response", err)
	}
	// Reconstruct a worker after the database committed but the reply was lost.
	w = &PublicationDescriptionReconciler{enabled: true, scope: w.scope, ledger: db, ports: publicationDescriptionReleaseOutcomePorts{ReadCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
		t.Fatal("recovery required Cloud again")
		return nil, nil
	}}}
	if err := w.RunOnce(context.Background()); err != nil || len(db.outcomes) != 2 || db.outcomes[1] != "recorded" || db.writes != 1 || calls.Load() != 1 {
		t.Fatal("local receipt recovery", db.outcomes, err)
	}
}

func TestSkillHubPublicationDescriptionReconcileFailsClosed(t *testing.T) {
	for _, mode := range []string{"wrong-scope", "wrong-application", "expired", "disabled", "lost-cloud", "nil-cloud", "cancel-peer", "settlement-error"} {
		t.Run(mode, func(t *testing.T) {
			w, db, calls := descriptionReconcileFixture(t, "published")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "wrong-scope":
				w.scope.Audience = "https://other.example"
			case "wrong-application":
				db.lease.ApplicationID = "pubdesc_" + strings.Repeat("f", 64)
			case "expired":
				db.lease.ExpiresAt = time.Now().Add(-time.Second)
			case "disabled":
				db.onRead = func(int) error { return store.ErrSkillHubPublicationDenied }
			case "lost-cloud":
				w.ports.ReadCloud = func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
					return nil, context.DeadlineExceeded
				}
			case "nil-cloud":
				w.ports.ReadCloud = func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
					return nil, nil
				}
			case "cancel-peer":
				w.ports.ReadCloud = func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
					cancel()
					return nil, ctx.Err()
				}
			case "settlement-error":
				db.finishErr = store.ErrSkillHubPublicationStale
			}
			err := w.RunOnce(ctx)
			if mode == "settlement-error" {
				if !errors.Is(err, store.ErrSkillHubPublicationStale) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || db.writes != 0 || len(db.outcomes) != 1 || db.outcomes[0] != "retry" {
				t.Fatal("unsafe terminal", db.outcomes, err)
			}
			if mode == "wrong-scope" || mode == "wrong-application" || mode == "expired" || mode == "disabled" {
				if calls.Load() != 0 {
					t.Fatal("unauthorized peer call")
				}
			}
		})
	}
}

func TestSkillHubPublicationDescriptionReconcileStatusOnlyHTTPS(t *testing.T) {
	var status publicationDescriptionCloudReleaseStatus
	calls := 0
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != publicationDescriptionReleaseStatusPath {
			t.Error("reconciler invoked a non-status endpoint")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer peer.Close()
	app, intent, permit := releaseAuthorizeClientFixture(t, peer.URL)
	status = releaseStatusFixture(app, permit)
	_, db, _ := descriptionReconcileFixture(t, "queued")
	cfg := derivationClientConfig(peer)
	cfg.ReleaseStatusEnabled = true
	w, err := NewPublicationDescriptionReconciler(true, cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.ports.ReadCloud(context.Background(), app, intent, permit)
	if err != nil || got == nil || got.State != "queued" || calls != 1 {
		t.Fatal("status-only transport", got, err)
	}
}

func TestSkillHubPublicationDescriptionReconcileGatesAndShutdown(t *testing.T) {
	w, db, _ := descriptionReconcileFixture(t, "queued")
	off, err := NewPublicationDescriptionReconciler(false, PublicationDescriptionDerivationClientConfig{}, db)
	if err != nil || off.RunOnce(context.Background()) != nil || db.claims.Load() != 0 {
		t.Fatal("disabled worker polled")
	}
	if _, err := NewPublicationDescriptionReconciler(true, PublicationDescriptionDerivationClientConfig{}, db); err == nil {
		t.Fatal("invalid enabled config")
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RECONCILE_ENABLED", "")
	StartSkillHubPublicationDescriptionReconcile(context.Background(), nil)()
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RECONCILE_ENABLED", "1")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED", "")
	StartSkillHubPublicationDescriptionReconcile(context.Background(), nil)()
	started := make(chan struct{}, 4)
	db.claim = func(ctx context.Context) (*store.SkillHubPublicationDescriptionReconcileLease, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("missing bounded concurrency")
		}
	}
	if err := w.RunOnce(ctx); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal("unbounded fanout", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not wait/exit")
	}
	if w.active.Load() != 0 || w.running.Load() {
		t.Fatal("active work leaked")
	}
}
