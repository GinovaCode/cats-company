package server

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openchat/openchat/server/store"
)

type PublicationCommitDispatcher struct {
	enabled bool
	scope   store.SkillHubPublicationCancellationScope
	ledger  store.SkillHubPublicationCommitDeliveryStore
	cloud   publicationHumanCommitCloud
	running atomic.Bool
	active  atomic.Int32
}

func NewPublicationCommitDispatcher(c PublicationCommitCloudConfig, ledger store.SkillHubPublicationCommitDeliveryStore) (*PublicationCommitDispatcher, error) {
	client, err := NewPublicationCommitCloudClient(c)
	if err != nil {
		return nil, err
	}
	if c.Enabled && ledger == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	return &PublicationCommitDispatcher{enabled: c.Enabled, scope: store.SkillHubPublicationCancellationScope{Issuer: c.Issuer, Audience: c.Audience}, ledger: ledger, cloud: client}, nil
}

// Separate from HTTP/WS request lifetime. An already saved human intent is the
// only queue admission; this worker never records intent or plans a new version.
func StartSkillHubPublicationCommitDispatch(parent context.Context, db store.Store) func() {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_COMMIT_DISPATCH_ENABLED"))
	if v != "1" && !strings.EqualFold(v, "true") {
		return func() {}
	}
	ledger, _ := db.(store.SkillHubPublicationCommitDeliveryStore)
	w, err := NewPublicationCommitDispatcher(PublicationCommitCloudConfig{Enabled: true,
		Issuer: strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_ISSUER")), Audience: strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE")),
		PeerSecret: os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET")}, ledger)
	if err != nil {
		log.Print("SkillHub publication commit dispatch unavailable: configuration or storage unsupported.")
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	return func() { cancel(); <-done }
}

func (w *PublicationCommitDispatcher) Run(ctx context.Context) {
	if !w.enabled || !w.running.CompareAndSwap(false, true) {
		return
	}
	defer w.running.Store(false)
	for ctx.Err() == nil {
		if err := w.RunOnce(ctx); err != nil {
			log.Print("SkillHub publication commit dispatch unavailable; retained work will be recovered.")
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (w *PublicationCommitDispatcher) RunOnce(ctx context.Context) error {
	if !w.enabled || ctx.Err() != nil {
		return nil
	}
	if w.active.Add(1) > 4 {
		w.active.Add(-1)
		return store.ErrSkillHubPublicationBusy
	}
	defer w.active.Add(-1)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	l, err := w.ledger.ClaimSkillHubPublicationCommitDelivery(ctx, w.scope)
	if err != nil || l == nil {
		return err
	}
	if l.Validate() != nil || l.Intent.Input.Receipt.Query.Issuer != w.scope.Issuer || l.Intent.Input.Receipt.Query.Audience != w.scope.Audience {
		return store.ErrSkillHubPublicationInvalid
	}
	o := w.deliver(ctx, *l)
	// A disconnect cannot erase a committed intent. If finishing fails or the
	// process dies, the 90-second SQL lease makes identical recovery possible.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	return w.ledger.FinishSkillHubPublicationCommitDelivery(finishCtx, *l, o)
}
func (w *PublicationCommitDispatcher) deliver(ctx context.Context, l store.SkillHubPublicationCommitLease) store.SkillHubPublicationCommitOutcome {
	r := l.Intent.Input.Receipt
	observed, err := w.cloud.Read(ctx, r)
	if err != nil {
		return publicationCommitDeliveryError(err)
	}
	if !validPublicationCommitObservation(observed, r) {
		return store.SkillHubPublicationCommitOutcome{Kind: "retry"}
	}
	if observed.State == "cancelled" {
		return store.SkillHubPublicationCommitOutcome{Kind: "cancelled"}
	}
	if observed.Target == nil || *observed.Target != l.Intent.Input.Target {
		return store.SkillHubPublicationCommitOutcome{Kind: "conflict"}
	}
	if publicationCloudCommitState(observed.State) {
		return store.SkillHubPublicationCommitOutcome{Kind: "accepted", PermitID: *observed.PermitID}
	}
	ack, err := w.cloud.Authorize(ctx, r)
	if err != nil {
		return publicationCommitDeliveryError(err)
	}
	if ack == nil || ack.SchemaVersion != 1 || ack.Receipt != publicationControlReceipt(r) || !publicationCloudCommitState(ack.State) || ack.PermitID == nil || !publicationPermitID.MatchString(*ack.PermitID) {
		return store.SkillHubPublicationCommitOutcome{Kind: "retry"}
	}
	return store.SkillHubPublicationCommitOutcome{Kind: "accepted", PermitID: *ack.PermitID}
}
func publicationCommitDeliveryError(err error) store.SkillHubPublicationCommitOutcome {
	kind := "retry"
	switch {
	case errors.Is(err, store.ErrSkillHubPublicationDenied):
		kind = "denied"
	case errors.Is(err, store.ErrSkillHubPublicationSettled):
		kind = "cancelled"
	case errors.Is(err, store.ErrSkillHubPublicationConflict), errors.Is(err, errPublicationCommitRejected), errors.Is(err, errPublicationCommitConfirmationRequired):
		kind = "conflict"
	case errors.Is(err, errPublicationCommitNotFound):
		kind = "not_found"
	}
	return store.SkillHubPublicationCommitOutcome{Kind: kind}
}
