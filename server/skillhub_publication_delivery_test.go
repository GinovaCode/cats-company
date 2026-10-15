package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type publicationDeliveryTestJob struct {
	lease       store.SkillHubPublicationDeliveryLeaseRecord
	state, code string
	next        time.Time
}
type publicationDeliveryTestStore struct {
	*publicationWorkspaceTestStore
	deliveryMu                   sync.Mutex
	jobs                         map[string]*publicationDeliveryTestJob
	claims, finishes, lists      int
	listErr, claimErr, finishErr error
	onClaim                      func()
}

func (s *publicationDeliveryTestStore) ListSkillHubPublicationDeliveries(ctx context.Context, bot int64, limit int) ([]string, error) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.lists++
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []string{}
	for _, e := range s.entries {
		if e.Input.BotUID == bot {
			j := s.jobs[e.ID]
			if j == nil || (j.state == "pending" && !time.Now().Before(j.next)) || (j.state == "leased" && !time.Now().Before(j.lease.ExpiresAt)) {
				ids = append(ids, e.ID)
			}
		}
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (s *publicationDeliveryTestStore) ListSkillHubPublicationRuntimeDeliveries(ctx context.Context, selector store.SkillHubPublicationRuntimeSelector, limit int) ([]string, error) {
	ids, err := s.ListSkillHubPublicationDeliveries(ctx, selector.BotUID, 1000)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, id := range ids {
		for _, e := range s.entries {
			if e.ID == id && e.Input.ActorUID == selector.ActorUID && e.Input.Source.DeviceID == selector.DeviceID && e.Input.Source.BodyID == selector.BodyID && e.Input.Source.InstallationID == selector.InstallationID {
				out = append(out, id)
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *publicationDeliveryTestStore) ClaimSkillHubPublicationDelivery(ctx context.Context, id string, b store.SkillHubPublicationDeliveryBinding) (*store.SkillHubPublicationDeliveryLeaseRecord, error) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.claims++
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if j := s.jobs[id]; j != nil && j.state == "leased" && time.Now().Before(j.lease.ExpiresAt) {
		return nil, store.ErrSkillHubPublicationBusy
	}
	l := store.SkillHubPublicationDeliveryLeaseRecord{EntryID: id, Binding: b, Token: fmt.Sprintf("%032x", s.claims), Attempt: s.claims, ExpiresAt: time.Now().Add(30 * time.Second)}
	s.jobs[id] = &publicationDeliveryTestJob{lease: l, state: "leased"}
	if s.onClaim != nil {
		s.onClaim()
	}
	return &l, nil
}
func (s *publicationDeliveryTestStore) FinishSkillHubPublicationDelivery(ctx context.Context, l store.SkillHubPublicationDeliveryLeaseRecord, code string) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.finishes++
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.finishErr != nil {
		return s.finishErr
	}
	j := s.jobs[l.EntryID]
	if j == nil || j.lease.Token != l.Token || j.lease.Binding != l.Binding || uint64(l.Binding.Source.RouteGeneration) != s.generation {
		return store.ErrSkillHubPublicationStale
	}
	state, valid := store.SkillHubPublicationDeliveryOutcome(code)
	if !valid {
		return store.ErrSkillHubPublicationInvalid
	}
	j.state, j.code, j.next = state, code, time.Now().Add(5*time.Second)
	return nil
}

func newPublicationDeliveryFixture(t *testing.T) (*publicationWorkspaceFixture, *publicationDeliveryTestStore, *publicationDeliveryWorker) {
	t.Helper()
	var s *publicationDeliveryTestStore
	f := newPublicationWorkspaceFixtureWithStore(t, func(base *publicationWorkspaceTestStore) store.Store {
		s = &publicationDeliveryTestStore{publicationWorkspaceTestStore: base, jobs: make(map[string]*publicationDeliveryTestJob)}
		return s
	})
	in := store.SkillHubPublicationEntryInput{ActorUID: 7, BotUID: 42, Entrypoint: "workspace", RequestID: "delivery-click", Source: store.SkillHubPublicationSource{DeviceID: f.device.DeviceID, BodyID: f.device.BodyID, InstallationID: f.device.InstallationID, RouteGeneration: 1, WorkspaceGeneration: "workspace-v1", LocalSkillID: "local-skill-1"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := s.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy()); err != nil {
		t.Fatal(err)
	}
	w := newPublicationDeliveryWorker(f.h)
	if w == nil {
		t.Fatal("missing worker")
	}
	f.h.publicationDelivery = w
	w.running.Store(true)
	t.Cleanup(w.cancel)
	return f, s, w
}

func deliveryProbe(t *testing.T, c *Client) *MsgThinToolRPC {
	t.Helper()
	var msg ServerMessage
	decodeQueuedServerMessage(t, c.send, &msg)
	if msg.ThinToolRPC == nil || !strings.HasPrefix(msg.ThinToolRPC.RequestID, publicationDeliveryRequestPrefix) {
		t.Fatalf("probe: %+v", msg)
	}
	return msg.ThinToolRPC
}
func deliveryProcess(t *testing.T, w *publicationDeliveryWorker) {
	t.Helper()
	select {
	case r := <-w.results:
		w.processResult(r)
	default:
		t.Fatal("no queued receipt")
	}
}
func deliveryAckStatus(t *testing.T, c *Client, want int) {
	t.Helper()
	var msg ServerMessage
	decodeQueuedServerMessage(t, c.send, &msg)
	if msg.Ctrl == nil || msg.Ctrl.Code != want {
		t.Fatalf("ack: %+v want %d", msg, want)
	}
}

func TestSkillHubPublicationDeliveryOwnerOffline(t *testing.T) {
	f, s, w := newPublicationDeliveryFixture(t)
	f.h.removeClient(f.owner)
	f.h.cancelThinToolRPCRequestsByRequesterRoute(f.h.clientRoute(f.owner))
	f.owner.closeSend()
	w.sweep()
	probe := deliveryProbe(t, f.runtime)
	if len(probe.Payload) != 3 || probe.ToolName != string(DeviceCapabilitySkillHubPublicationProbe) || probe.TargetOwnerUserID != "usr7" {
		t.Fatalf("payload: %+v", probe)
	}
	if strings.Contains(fmt.Sprint(probe.Payload), "pubentry_") || strings.Contains(probe.RequestID, s.jobs[publicationReadTestID].lease.Token) {
		t.Fatal("durable identity/token leaked")
	}
	f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
	deliveryProcess(t, w)
	deliveryAckStatus(t, f.runtime, 200)
	if s.jobs[publicationReadTestID].state != "source_verified" || s.creates != 1 {
		t.Fatal("receipt did not settle original intent")
	}
	w.sweep()
	if len(f.runtime.send) != 0 {
		t.Fatal("verified source dispatched again")
	}
	f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
	deliveryAckStatus(t, f.runtime, 404)
}

func TestSkillHubPublicationDeliveryAuthorityAndSourceGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*publicationWorkspaceFixture, *publicationDeliveryTestStore)
	}{
		{"owner-disabled", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) { s.users[7].State = 1 }},
		{"bot-disabled", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) { s.config.Enabled = false }},
		{"transfer", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) { s.config.OwnerID = 9 }},
		{"body", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) { s.config.BodyID = "another" }},
		{"installation", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) {
			f.runtime.installationID = "another"
		}},
		{"generation", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) { s.generation = 2 }},
		{"source-device", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) {
			s.entries["delivery-click"].Input.Source.DeviceID = "another"
		}},
		{"source-install", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) {
			s.entries["delivery-click"].Input.Source.InstallationID = "another"
		}},
		{"legacy-capability", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) {
			f.h.userDevices.mu.Lock()
			d := f.h.userDevices.devices[7][f.device.DeviceID]
			d.Capabilities = nil
			f.h.userDevices.devices[7][f.device.DeviceID] = d
			f.h.userDevices.mu.Unlock()
		}},
		{"offline", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) { f.h.removeClient(f.runtime) }},
		{"wiki", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) { f.runtime.wikiAgentUID = 42 }},
		{"db-outage", func(f *publicationWorkspaceFixture, s *publicationDeliveryTestStore) {
			s.generationErr = errors.New("private DB failure")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, w := newPublicationDeliveryFixture(t)
			tc.change(f, s)
			w.sweep()
			if s.claims != 0 || len(f.runtime.send) != 0 {
				t.Fatal("unsafe dispatch")
			}
		})
	}
}

func TestSkillHubPublicationDeliveryPostClaimChange(t *testing.T) {
	f, s, w := newPublicationDeliveryFixture(t)
	s.onClaim = func() { s.config.OwnerID = 9 }
	w.sweep()
	if s.claims != 1 || len(f.runtime.send) != 0 || len(w.pending) != 0 {
		t.Fatal("post-claim authority not rechecked")
	}
}

func TestSkillHubPublicationDeliveryReceiptGuards(t *testing.T) {
	for _, kind := range []string{"human", "wrong-socket", "wrong-source", "extra-field", "raw-error", "owner-transfer", "new-generation", "new-workspace", "mixed-result"} {
		t.Run(kind, func(t *testing.T) {
			f, s, w := newPublicationDeliveryFixture(t)
			w.sweep()
			probe := deliveryProbe(t, f.runtime)
			reply := f.result(probe)
			caller := f.runtime
			wantAck := 200
			wantState := "needs_user_action"
			switch kind {
			case "human":
				caller = f.owner
				wantAck = 403
				wantState = "leased"
			case "wrong-socket":
				caller = &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
				wantAck = 403
				wantState = "leased"
			case "wrong-source":
				reply.Result.(map[string]interface{})["local_skill_id"] = "other"
			case "extra-field":
				reply.Result.(map[string]interface{})["raw_path"] = "/private/file"
			case "raw-error":
				reply.Result = nil
				reply.Error = &MsgDeviceRPCError{Code: "raw-secret", Message: "secret"}
				wantState = "pending"
			case "owner-transfer":
				s.config.OwnerID = 9
				wantAck = 409
				wantState = "leased"
			case "new-generation":
				s.generation = 2
				wantAck = 409
				wantState = "leased"
			case "new-workspace":
				reply.Result.(map[string]interface{})["workspace_generation"] = "changed"
			case "mixed-result":
				reply.Error = &MsgDeviceRPCError{Code: "publication.runtime_timeout"}
			}
			f.h.handleThinToolRPCResult(caller, reply)
			if wantAck != 403 {
				deliveryProcess(t, w)
			}
			deliveryAckStatus(t, caller, wantAck)
			if s.jobs[publicationReadTestID].state != wantState {
				t.Fatalf("state=%s want %s", s.jobs[publicationReadTestID].state, wantState)
			}
			if strings.Contains(s.jobs[publicationReadTestID].code, "secret") {
				t.Fatal("raw error persisted")
			}
		})
	}
}

func TestSkillHubPublicationDeliveryTimeoutAndBackpressure(t *testing.T) {
	f, s, w := newPublicationDeliveryFixture(t)
	w.sweep()
	probe := deliveryProbe(t, f.runtime)
	p := w.pending[probe.RequestID]
	p.deadline = time.Now().Add(-time.Second)
	w.queueTimeouts(time.Now())
	deliveryProcess(t, w)
	if s.jobs[publicationReadTestID].state != "pending" || s.jobs[publicationReadTestID].code != "publication.runtime_timeout" {
		t.Fatal("timeout not persisted")
	}
	f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
	deliveryAckStatus(t, f.runtime, 404)
	w.sweep()
	if s.claims != 1 {
		t.Fatal("ignored backoff")
	}
	s.jobs[publicationReadTestID].next = time.Now().Add(-time.Second)
	for i := 0; i < cap(f.runtime.send); i++ {
		f.runtime.send <- []byte("occupied")
	}
	w.sweep()
	deliveryProcess(t, w)
	if !f.h.isClientRegistered(f.runtime) || f.runtime.sendClosed {
		t.Fatal("publication backpressure disconnected Bot")
	}
	if s.jobs[publicationReadTestID].code != "publication.runtime_unavailable" {
		t.Fatal("full queue not retryable")
	}
}

func TestSkillHubPublicationDeliveryDuplicateReceipt(t *testing.T) {
	f, s, w := newPublicationDeliveryFixture(t)
	w.sweep()
	probe := deliveryProbe(t, f.runtime)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.h.handleThinToolRPCResult(f.runtime, f.result(probe)) }()
	}
	wg.Wait()
	if len(w.results) != 1 {
		t.Fatal("duplicate receipt enqueued")
	}
	deliveryProcess(t, w)
	if s.finishes != 1 {
		t.Fatal("duplicate DB finish")
	}
}

func TestSkillHubPublicationDeliveryStorageFailure(t *testing.T) {
	for _, stage := range []string{"list", "claim", "finish"} {
		t.Run(stage, func(t *testing.T) {
			f, s, w := newPublicationDeliveryFixture(t)
			err := errors.New("DB secret")
			switch stage {
			case "list":
				s.listErr = err
			case "claim":
				s.claimErr = err
			case "finish":
				s.finishErr = err
			}
			w.sweep()
			if stage != "finish" {
				if len(f.runtime.send) != 0 {
					t.Fatal("dispatch on failed storage")
				}
				return
			}
			probe := deliveryProbe(t, f.runtime)
			f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
			deliveryProcess(t, w)
			deliveryAckStatus(t, f.runtime, 503)
			if s.jobs[publicationReadTestID].state != "leased" {
				t.Fatal("uncertain write fabricated success")
			}
		})
	}
}

func TestSkillHubPublicationDeliveryFlagsAndLifecycle(t *testing.T) {
	f, _, w := newPublicationDeliveryFixture(t)
	for _, key := range []string{"CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", "CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED", "CATSCO_SKILLHUB_PUBLICATION_DELIVERY_ENABLED"} {
		t.Setenv(key, "")
	}
	if newPublicationDeliveryWorkerFromEnv(f.h) != nil {
		t.Fatal("default enabled")
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DELIVERY_ENABLED", "true")
	if newPublicationDeliveryWorkerFromEnv(f.h) != nil {
		t.Fatal("missing admission flags accepted")
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", "1")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED", "true")
	enabled := newPublicationDeliveryWorkerFromEnv(f.h)
	if enabled == nil {
		t.Fatal("not enabled")
	}
	enabled.cancel()
	if newPublicationDeliveryWorker(NewHub(f.db, nil)) != nil {
		t.Fatal("unsupported adapter enabled")
	}
	w.running.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.h.RunPublicationDeliveries(ctx)
	w.sweep()
	if len(f.runtime.send) != 0 {
		t.Fatal("dispatch after cancelled startup")
	}
	f.h.handleThinToolRPCResult(f.runtime, &MsgThinToolRPC{RequestID: publicationDeliveryRequestPrefix + "late"})
	deliveryAckStatus(t, f.runtime, http.StatusServiceUnavailable)
}

func TestSkillHubPublicationDeliveryRunningLifecycle(t *testing.T) {
	f, s, w := newPublicationDeliveryFixture(t)
	w.running.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { f.h.RunPublicationDeliveries(ctx); close(done) }()
	var msg ServerMessage
	select {
	case data := <-f.runtime.send:
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startup sweep did not run")
	}
	if msg.ThinToolRPC == nil {
		t.Fatal("missing startup probe")
	}
	f.h.handleThinToolRPCResult(f.runtime, f.result(msg.ThinToolRPC))
	select {
	case data := <-f.runtime.send:
		var ack ServerMessage
		if err := json.Unmarshal(data, &ack); err != nil || ack.Ctrl == nil || ack.Ctrl.Code != 200 {
			t.Fatalf("receipt: %+v %v", ack, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("receipt worker did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker failed to stop")
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if s.jobs[publicationReadTestID].state != "source_verified" {
		t.Fatal("async receipt not saved")
	}
	if w.running.Load() {
		t.Fatal("worker still running")
	}
}

func TestSkillHubPublicationDeliveryBoundsAndRotation(t *testing.T) {
	f, s, w := newPublicationDeliveryFixture(t)
	for i := 0; i < publicationDeliveryPendingLimit; i++ {
		w.pending[fmt.Sprint(i)] = &publicationDeliveryPending{}
	}
	w.sweep()
	if s.claims != 0 {
		t.Fatal("claimed beyond pending cap")
	}
	w.pending = make(map[string]*publicationDeliveryPending)
	for i := int64(100); i < 132; i++ {
		c := &Client{uid: i, accountType: types.AccountBot, deviceOwnerUID: 7, deviceID: fmt.Sprint(i), send: make(chan []byte, 1)}
		f.h.addClient(c)
	}
	first := w.localBots()
	if len(first) != publicationDeliveryBotBatch {
		t.Fatal("unbounded bot batch")
	}
	w.cursor = first[len(first)-1].uid
	second := w.localBots()
	if second[0].uid <= first[len(first)-1].uid {
		t.Fatal("starved later bots")
	}
}
