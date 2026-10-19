package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type commitDispatchCloud struct {
	read      func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error)
	authorize func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error)
}

func (*commitDispatchCloud) Plan(context.Context, store.SkillHubPublicationCloudReceipt, bool) (*store.SkillHubPublicationCommitInput, error) {
	panic("dispatcher must never plan")
}
func (c *commitDispatchCloud) Read(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
	return c.read(ctx, r)
}
func (c *commitDispatchCloud) Authorize(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
	return c.authorize(ctx, r)
}

type commitDispatchLedger struct {
	mu                  sync.Mutex
	lease               *store.SkillHubPublicationCommitLease
	claims              int
	out                 []store.SkillHubPublicationCommitOutcome
	claimErr, finishErr error
}

func (s *commitDispatchLedger) ClaimSkillHubPublicationCommitDelivery(ctx context.Context, scope store.SkillHubPublicationCancellationScope) (*store.SkillHubPublicationCommitLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	return s.lease, s.claimErr
}
func (s *commitDispatchLedger) FinishSkillHubPublicationCommitDelivery(ctx context.Context, l store.SkillHubPublicationCommitLease, o store.SkillHubPublicationCommitOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out = append(s.out, o)
	return s.finishErr
}
func (*commitDispatchLedger) GetSkillHubPublicationCommitDelivery(context.Context, int64, string) (*store.SkillHubPublicationCommitDelivery, error) {
	panic("dispatcher must not read human status")
}
func commitDispatchFixture() (*PublicationCommitDispatcher, *commitDispatchLedger, *humanCommitCloud) {
	_, c, _ := humanCommitFixture()
	hash, _ := c.in.IdentityHash()
	l := &store.SkillHubPublicationCommitLease{Intent: store.SkillHubPublicationCommitIntent{Input: c.in, IdentityHash: hash, RequestedAt: time.Now()}, Token: strings.Repeat("a", 32), Attempt: 1, ExpiresAt: time.Now().Add(90 * time.Second)}
	db := &commitDispatchLedger{lease: l}
	w := &PublicationCommitDispatcher{enabled: true, scope: store.SkillHubPublicationCancellationScope{Issuer: c.in.Receipt.Query.Issuer, Audience: c.in.Receipt.Query.Audience}, ledger: db, cloud: c}
	return w, db, c
}
func TestSkillHubPublicationCommitDispatcherOutcomes(t *testing.T) {
	for _, mode := range []string{"held", "committing", "publish_queued", "publish_failed", "published", "cancelled", "wrong_target", "missing_target", "bad_observation", "bad_ack", "denied", "conflict", "not_found", "rejected", "confirmation", "transient", "cancelled_error"} {
		t.Run(mode, func(t *testing.T) {
			w, db, c := commitDispatchFixture()
			calls := 0
			want := "accepted"
			c.plan = func(context.Context) error { t.Fatal("planned again"); return nil }
			c.authorize = func(context.Context) error { calls++; return nil }
			switch mode {
			case "committing", "publish_queued", "publish_failed":
				c.state = mode
			case "published":
				c.state = mode
				c.result = &PublicationCommitCloudResult{SkillID: c.in.Target.SkillID, Version: c.in.Target.Version, PackageID: "pkg_11111111-1111-4111-8111-111111111111", Checksum: strings.Repeat("b", 64), PublishedAt: c.in.Target.ReleaseAt}
			case "cancelled":
				c.state = mode
				want = "cancelled"
			case "wrong_target":
				c.in.Target.Version = "9.0.0"
				want = "conflict"
			case "bad_observation":
				c.state = "mystery"
				want = "retry"
			case "denied", "conflict", "not_found", "rejected", "confirmation", "transient", "cancelled_error":
				err := map[string]error{"denied": store.ErrSkillHubPublicationDenied, "conflict": store.ErrSkillHubPublicationConflict, "not_found": errPublicationCommitNotFound, "rejected": errPublicationCommitRejected, "confirmation": errPublicationCommitConfirmationRequired, "transient": errPublicationCommitCloud, "cancelled_error": store.ErrSkillHubPublicationSettled}[mode]
				c.authorize = func(context.Context) error { calls++; return err }
				want = map[string]string{"denied": "denied", "conflict": "conflict", "not_found": "not_found", "rejected": "conflict", "confirmation": "conflict", "transient": "retry", "cancelled_error": "cancelled"}[mode]
			case "missing_target":
				want = "conflict"
				w.cloud = &commitDispatchCloud{read: func(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
					s, e := c.Read(ctx, r)
					s.Target = nil
					return s, e
				}}
			case "bad_ack":
				want = "retry"
				w.cloud = &commitDispatchCloud{read: c.Read, authorize: func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
					calls++
					return &PublicationCommitCloudStatus{}, nil
				}}
			}
			if err := w.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(db.out) != 1 || db.out[0].Kind != want || db.out[0].Validate() != nil {
				t.Fatal(db.out)
			}
			if mode == "held" && calls != 1 {
				t.Fatal("held intent not delivered")
			}
			if (publicationCloudCommitState(mode) || mode == "cancelled" || mode == "wrong_target" || mode == "bad_observation") && calls != 0 {
				t.Fatal("unexpected authorization")
			}
		})
	}
}
func TestSkillHubPublicationCommitDispatcherGuards(t *testing.T) {
	w, db, _ := commitDispatchFixture()
	disabled, e := NewPublicationCommitDispatcher(PublicationCommitCloudConfig{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = disabled.RunOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = NewPublicationCommitDispatcher(PublicationCommitCloudConfig{Enabled: true}, db); e == nil {
		t.Fatal("invalid config")
	}
	if _, e = NewPublicationCommitDispatcher(PublicationCommitCloudConfig{Enabled: true, Issuer: w.scope.Issuer, Audience: w.scope.Audience, PeerSecret: strings.Repeat("a", 64)}, nil); e == nil {
		t.Fatal("missing ledger")
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_COMMIT_DISPATCH_ENABLED", "")
	StartSkillHubPublicationCommitDispatch(context.Background(), nil)()
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_COMMIT_DISPATCH_ENABLED", "true")
	StartSkillHubPublicationCommitDispatch(context.Background(), nil)()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = w.RunOnce(ctx); e != nil || db.claims != 0 {
		t.Fatal("cancelled claim", e)
	}
	w.scope.Audience = "https://unrelated.example"
	if e = w.RunOnce(context.Background()); !errors.Is(e, store.ErrSkillHubPublicationInvalid) || len(db.out) != 0 {
		t.Fatal("scope", e)
	}
	w.scope.Audience = db.lease.Intent.Input.Receipt.Query.Audience
	db.lease.Token = "bad"
	if e = w.RunOnce(context.Background()); !errors.Is(e, store.ErrSkillHubPublicationInvalid) {
		t.Fatal(e)
	}
	db.lease = nil
	if e = w.RunOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	db.claimErr = store.ErrSkillHubPublicationBusy
	if e = w.RunOnce(context.Background()); !errors.Is(e, db.claimErr) {
		t.Fatal(e)
	}
}
func TestSkillHubPublicationCommitDispatcherCancellationAndFinishFailure(t *testing.T) {
	w, db, c := commitDispatchFixture()
	ctx, cancel := context.WithCancel(context.Background())
	c.authorize = func(context.Context) error { cancel(); return context.Canceled }
	if e := w.RunOnce(ctx); e != nil || len(db.out) != 1 || db.out[0].Kind != "retry" {
		t.Fatal("disconnect lost bookkeeping", e)
	}
	db.finishErr = store.ErrSkillHubPublicationStale
	if e := w.RunOnce(context.Background()); !errors.Is(e, db.finishErr) {
		t.Fatal("finish failure swallowed", e)
	}
}
func TestSkillHubPublicationCommitDispatcherConcurrencyAndStop(t *testing.T) {
	w, db, c := commitDispatchFixture()
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	c.read = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := w.RunOnce(context.Background()); e != nil {
				t.Error(e)
			}
		}()
	}
	for n := 0; n < 4; n++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not enter")
		}
	}
	if e := w.RunOnce(context.Background()); !errors.Is(e, store.ErrSkillHubPublicationBusy) {
		t.Fatal("unbounded", e)
	}
	close(release)
	wg.Wait()
	if len(db.out) != 4 {
		t.Fatal("lost slots")
	}
	db.lease = nil
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop waited for timer")
	}
}
