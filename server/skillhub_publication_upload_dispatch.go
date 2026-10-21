package server

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openchat/openchat/server/store"
)

// publicationUploadDispatcher is the durable bridge between a captured
// workspace candidate and the dedicated XiaoBa upload RPC. Runtime ACKs only
// settle this lane as "uploaded"; Cloud acceptance is recorded separately by
// the authenticated receipt handler.
type publicationUploadDispatcher struct {
	h       *Hub
	ledger  store.SkillHubPublicationUploadDeliveryStore
	entries store.SkillHubPublicationEntryStore
	running atomic.Bool
	sweepMu sync.Mutex
	cursor  int64
}

func newPublicationUploadDispatcherFromEnv(h *Hub) *publicationUploadDispatcher {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_UPLOAD_DISPATCH_ENABLED"))
	if h == nil || h.publicationUpload == nil || (v != "1" && !strings.EqualFold(v, "true")) {
		return nil
	}
	ledger, ok := h.db.(store.SkillHubPublicationUploadDeliveryStore)
	if !ok {
		return nil
	}
	entries, ok := h.db.(store.SkillHubPublicationEntryStore)
	if !ok {
		return nil
	}
	return &publicationUploadDispatcher{h: h, ledger: ledger, entries: entries}
}

// Start only after the final Hub has been selected. Leases are durable, so a
// process crash or reconnect is recovered by a later sweep.
func StartSkillHubPublicationUploadDispatch(parent context.Context, h *Hub) func() {
	w := newPublicationUploadDispatcherFromEnv(h)
	if w == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (w *publicationUploadDispatcher) run(ctx context.Context) {
	if !w.running.CompareAndSwap(false, true) {
		return
	}
	defer w.running.Store(false)
	for ctx.Err() == nil {
		if err := w.runOnce(ctx); err != nil && ctx.Err() == nil {
			log.Print("SkillHub publication upload dispatch unavailable; retained attempts will recover.")
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

type publicationUploadJob struct {
	id      string
	binding store.SkillHubPublicationDeliveryBinding
}

func (w *publicationUploadDispatcher) runtimeBinding(actor, bot int64, device string) (store.SkillHubPublicationDeliveryBinding, bool) {
	source, route, _, code := w.h.publicationRuntimeBinding(actor, bot, device)
	if code != "" {
		return store.SkillHubPublicationDeliveryBinding{}, false
	}
	d, ok := w.h.userDevices.activeDevice(actor, device)
	if !ok {
		return store.SkillHubPublicationDeliveryBinding{}, false
	}
	for _, capability := range d.Capabilities {
		if capability == DeviceCapabilitySkillHubPublicationUpload {
			return store.SkillHubPublicationDeliveryBinding{
				ActorUID: actor, BotUID: bot, Source: source,
				NodeID: route.NodeID, ConnectionID: route.ConnectionID,
			}, true
		}
	}
	return store.SkillHubPublicationDeliveryBinding{}, false
}

func (w *publicationUploadDispatcher) runOnce(parent context.Context) error {
	if parent.Err() != nil || !w.sweepMu.TryLock() {
		return nil
	}
	defer w.sweepMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	jobs, err := w.selectJobs(ctx)
	cancel()
	if err != nil {
		return err
	}
	queue := make(chan publicationUploadJob, len(jobs))
	for _, job := range jobs {
		queue <- job
	}
	close(queue)
	var wg sync.WaitGroup
	var errsMu sync.Mutex
	var firstErr error
	for n := 0; n < 4 && n < len(jobs); n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				if parent.Err() != nil {
					return
				}
				if err := w.deliver(parent, job); err != nil {
					errsMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errsMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func (w *publicationUploadDispatcher) selectJobs(ctx context.Context) ([]publicationUploadJob, error) {
	jobs := []publicationUploadJob{}
	for _, bot := range w.h.publicationLocalBots(w.cursor) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		w.cursor = bot.uid
		binding, ok := w.runtimeBinding(bot.owner, bot.uid, bot.device)
		if !ok {
			continue // Offline/unsupported runtimes consume no attempt.
		}
		ids, err := w.ledger.ListSkillHubPublicationUploadDeliveries(ctx, store.SkillHubPublicationRuntimeSelector{
			ActorUID: binding.ActorUID, BotUID: binding.BotUID, DeviceID: binding.Source.DeviceID,
			BodyID: binding.Source.BodyID, InstallationID: binding.Source.InstallationID,
		}, 4)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			entry, err := w.entries.GetSkillHubPublicationEntry(ctx, binding.ActorUID, id)
			if err != nil {
				return nil, err
			}
			if entry == nil || entry.ID != id || entry.State != "waiting_runtime" ||
				entry.Input.Validate() != nil || entry.Input.ActorUID != binding.ActorUID ||
				entry.Input.BotUID != binding.BotUID || entry.Input.Source.DeviceID != binding.Source.DeviceID {
				continue
			}
			next := binding
			next.Source.LocalSkillID = entry.Input.Source.LocalSkillID
			next.Source.WorkspaceGeneration = entry.Input.Source.WorkspaceGeneration
			expected := entry.Input.Source
			if next.Source.RouteGeneration < expected.RouteGeneration {
				continue
			}
			expected.RouteGeneration = next.Source.RouteGeneration
			if next.Source != expected {
				continue
			}
			jobs = append(jobs, publicationUploadJob{id: id, binding: next})
			if len(jobs) == 8 {
				return jobs, nil
			}
		}
	}
	return jobs, nil
}

func (w *publicationUploadDispatcher) deliver(ctx context.Context, j publicationUploadJob) error {
	fresh, ok := w.runtimeBinding(j.binding.ActorUID, j.binding.BotUID, j.binding.Source.DeviceID)
	if !ok {
		return nil
	}
	fresh.Source.LocalSkillID = j.binding.Source.LocalSkillID
	fresh.Source.WorkspaceGeneration = j.binding.Source.WorkspaceGeneration
	if fresh != j.binding {
		return nil
	}
	lease, err := w.ledger.ClaimSkillHubPublicationUploadDelivery(ctx, j.id, j.binding)
	if errors.Is(err, store.ErrSkillHubPublicationBusy) ||
		errors.Is(err, store.ErrSkillHubPublicationSettled) ||
		errors.Is(err, store.ErrSkillHubPublicationExhausted) ||
		errors.Is(err, store.ErrSkillHubPublicationDenied) ||
		errors.Is(err, store.ErrSkillHubPublicationStale) {
		return nil
	}
	if err != nil {
		return err
	}
	if lease == nil || lease.EntryID != j.id || lease.Binding != j.binding || lease.Validate() != nil {
		return store.ErrSkillHubPublicationInvalid
	}
	workCtx, cancel := context.WithDeadline(ctx, lease.ExpiresAt.Add(-5*time.Second))
	uploadErr := w.h.UploadSkillHubPublication(workCtx, j.binding.ActorUID, j.id)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return w.ledger.FinishSkillHubPublicationUploadDelivery(ctx, *lease, publicationUploadOutcome(uploadErr))
}

func publicationUploadOutcome(err error) string {
	switch {
	case err == nil:
		return "uploaded"
	case errors.Is(err, store.ErrSkillHubPublicationInvalid),
		errors.Is(err, store.ErrSkillHubPublicationDenied),
		errors.Is(err, store.ErrSkillHubPublicationConflict),
		errors.Is(err, store.ErrSkillHubPublicationSettled):
		return "rejected"
	default:
		return "retry"
	}
}
