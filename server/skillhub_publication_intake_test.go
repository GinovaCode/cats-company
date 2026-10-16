package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func newPublicationIntakeFixture(t *testing.T) (*publicationWorkspaceFixture, *publicationDeliveryTestStore, *publicationDeliveryWorker) {
	t.Helper()
	return newPublicationIntakeFixtureWithStore(t, nil)
}

func newPublicationIntakeFixtureWithStore(t *testing.T, wrap func(*publicationDeliveryTestStore) store.Store) (*publicationWorkspaceFixture, *publicationDeliveryTestStore, *publicationDeliveryWorker) {
	t.Helper()
	var s *publicationDeliveryTestStore
	f := newPublicationWorkspaceFixtureWithStore(t, func(base *publicationWorkspaceTestStore) store.Store {
		s = &publicationDeliveryTestStore{publicationWorkspaceTestStore: base, jobs: make(map[string]*publicationDeliveryTestJob)}
		if wrap != nil {
			return wrap(s)
		}
		return s
	})
	w := newPublicationDeliveryWorker(f.h)
	if w == nil {
		t.Fatal("missing worker")
	}
	f.h.publicationDelivery = w
	w.running.Store(true)
	t.Cleanup(w.cancel)
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for len(f.h.publicationAdmissions) != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(f.h.publicationAdmissions) != 0 {
			t.Error("admission did not release its bounded slot")
		}
	})
	return f, s, w
}

func publicationIntakeRPC() *MsgThinToolRPC {
	r := publicationWorkspaceRequest()
	r.ToolName = publicationWorkspaceCreateDurable
	return r
}

func (s *publicationDeliveryTestStore) GetSkillHubPublicationSourceStatus(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationSourceStatus, error) {
	e, err := s.GetSkillHubPublicationEntry(ctx, actor, id)
	if err != nil || e == nil {
		return nil, err
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	now := time.Now()
	r := &store.SkillHubPublicationSourceStatus{EntryID: id, ActorUID: actor, State: "pending", NextAttemptAt: now, ObservedAt: now}
	if j := s.jobs[id]; j != nil {
		r.State, r.Attempts, r.LastCode = j.state, j.lease.Attempt, j.code
		r.LeaseUntil = &j.lease.ExpiresAt
		if !j.next.IsZero() {
			r.NextAttemptAt = j.next
		}
		if j.state == "source_verified" {
			r.SourceVerifiedAt = &now
		}
	}
	return r, nil
}

func publicationIntakeResponse(t *testing.T, c *Client) *MsgThinToolRPC {
	t.Helper()
	var msg ServerMessage
	decodeQueuedServerMessageEventually(t, c.send, &msg)
	if msg.ThinToolRPC == nil || msg.ThinToolRPC.ToolName != publicationWorkspaceCreateDurable {
		t.Fatalf("expected v2 result, got %+v", msg)
	}
	return msg.ThinToolRPC
}

func TestSkillHubPublicationWorkspaceDurableAdmissionBeforeFirstProbe(t *testing.T) {
	f, s, w := newPublicationIntakeFixture(t)
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	reply := publicationIntakeResponse(t, f.owner)
	if reply.Error != nil || reply.RequestID != "click-0001" {
		t.Fatalf("admission: %+v", reply)
	}
	result := reply.Result.(map[string]interface{})
	if len(result) != 4 || result["schema_version"] != float64(2) || result["state"] != "waiting_runtime" || result["replay"] != false {
		t.Fatalf("unexpected result: %v", result)
	}
	if len(f.runtime.send) != 0 || s.claims != 0 || len(s.jobs) != 0 {
		t.Fatal("admission performed a transient probe")
	}
	e, err := s.GetSkillHubPublicationEntryByRequest(context.Background(), 7, "click-0001")
	if err != nil || e == nil || e.ID != result["entry_request_id"] {
		t.Fatal("success reported before durable intent existed")
	}
	// Losing the browser before the FIRST probe no longer loses the intent.
	f.h.removeClient(f.owner)
	w.sweep()
	probe := deliveryProbe(t, f.runtime)
	f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
	deliveryProcess(t, w)
	deliveryAckStatus(t, f.runtime, 200)
	if s.jobs[e.ID].state != "source_verified" || e.State != "waiting_runtime" || len(f.owner.send) != 0 {
		t.Fatal("background completion lost or mislabeled as publication")
	}
}

func TestSkillHubPublicationWorkspaceDurableReplayAfterReconnect(t *testing.T) {
	f, s, w := newPublicationIntakeFixture(t)
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	first := publicationIntakeResponse(t, f.owner)
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	// A legitimate reconnect advances the trusted generation, not user input.
	s.generation = 2
	f.h.mu.Lock()
	f.h.botConnectionEpochs[42] = 2
	f.h.mu.Unlock()
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	replay := publicationIntakeResponse(t, f.owner)
	if replay.Error != nil || replay.Result.(map[string]interface{})["replay"] != true {
		t.Fatalf("replay: %+v", replay)
	}
	if len(s.entries) != 1 || s.entries["click-0001"].Input.Source.RouteGeneration != 1 {
		t.Fatal("replay replaced immutable evidence")
	}
	w.sweep()
	probe := deliveryProbe(t, f.runtime)
	f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
	deliveryProcess(t, w)
	deliveryAckStatus(t, f.runtime, 200)
	job := s.jobs[publicationReadTestID]
	if job.lease.Binding.Source.RouteGeneration != 2 || job.state != "source_verified" {
		t.Fatal("worker failed to reauthorize new connection")
	}
	// A replay of a completed probe must not reset/requeue it.
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	if reply := publicationIntakeResponse(t, f.owner); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	w.sweep()
	if s.claims != 1 || len(f.runtime.send) != 0 {
		t.Fatal("replay reset the delivery ledger")
	}
}

func TestSkillHubPublicationWorkspaceDurableCancelledReplay(t *testing.T) {
	f, s, _ := newPublicationIntakeFixture(t)
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	first := publicationIntakeResponse(t, f.owner)
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	s.mu.Lock()
	s.entries["click-0001"].State = "cancelled"
	s.mu.Unlock()
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	reply := publicationIntakeResponse(t, f.owner)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	r := reply.Result.(map[string]interface{})
	if r["state"] != "cancelled" || r["replay"] != true || r["entry_request_id"] != publicationReadTestID || len(f.runtime.send) != 0 || len(s.entries) != 1 {
		t.Fatalf("cancelled replay: %v", r)
	}
}

func TestSkillHubPublicationWorkspaceDurableRequestGuards(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*publicationWorkspaceFixture, *publicationDeliveryWorker, *MsgThinToolRPC)
	}{
		{"disabled", "publication.disabled", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.h.publicationWorkspaceEntries = false
		}},
		{"worker disabled", "publication.disabled", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.h.publicationDelivery = nil
		}},
		{"worker stopped", "publication.unavailable", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			w.running.Store(false)
		}},
		{"shutdown", "publication.unavailable", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) { w.cancel() }},
		{"friend", "publication.owner_required", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.db.config.OwnerID = 9
		}},
		{"disabled human", "publication.owner_required", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.db.users[7].State = 1
		}},
		{"bot caller", "publication.owner_required", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.owner.accountType = types.AccountBot
		}},
		{"wiki", "publication.owner_required", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.owner.wikiAgentUID = 42
		}},
		{"connector", "publication.owner_required", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.owner.deviceConnector = &DeviceConnectorClaims{}
		}},
		{"body changed", "publication.runtime_changed", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.db.config.BodyID = "other-body"
		}},
		{"offline first request", "publication.runtime_unavailable", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			f.h.userDevices.unregister(7, "runtime-42")
		}},
		{"forged identity", "publication.invalid_request", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			r.Payload["owner_uid"] = "7"
		}},
		{"wrong owner header", "publication.invalid_request", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			r.TargetOwnerUserID = "usr8"
		}},
		{"numeric bot", "publication.invalid_request", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			r.Payload["bot_uid"] = 42
		}},
		{"path", "publication.invalid_request", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			r.Payload["local_skill_id"] = "/root/private"
		}},
		{"device mismatch", "publication.invalid_request", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			r.DeviceID = "other"
		}},
		{"expiry", "publication.invalid_request", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			r.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
		}},
		{"cross node", "publication.route_unsupported", func(f *publicationWorkspaceFixture, w *publicationDeliveryWorker, r *MsgThinToolRPC) {
			shared := newSharedMemoryRuntimeState()
			f.h.sharedRuntime = shared
			shared.bindUserDeviceRoute(7, f.device, runtimeRoute{NodeID: "other-node", ConnectionID: "remote-connection", ExpiresAt: time.Now().Add(time.Minute)}, time.Now())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, w := newPublicationIntakeFixture(t)
			r := publicationIntakeRPC()
			tc.change(f, w, r)
			f.h.handleThinToolRPCRequest(f.owner, r)
			reply := publicationIntakeResponse(t, f.owner)
			if reply.Error == nil || reply.Error.Code != tc.code || s.creates != 0 || len(f.runtime.send) != 0 {
				t.Fatalf("reply=%+v creates=%d", reply, s.creates)
			}
		})
	}
}

func TestSkillHubPublicationWorkspaceDurableAdmissionFailures(t *testing.T) {
	for _, problem := range []error{store.ErrSkillHubPublicationDenied, store.ErrSkillHubPublicationConflict, store.ErrSkillHubPublicationCapacity, store.ErrSkillHubPublicationRateLimit, store.ErrSkillHubPublicationInvalid, errors.New("private token /root/secret")} {
		t.Run(publicationAdmissionErrorCode(problem), func(t *testing.T) {
			f, s, _ := newPublicationIntakeFixture(t)
			s.createErr = problem
			f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
			reply := publicationIntakeResponse(t, f.owner)
			if reply.Error == nil || reply.Error.Code != publicationAdmissionErrorCode(problem) || len(s.entries) != 0 || len(f.runtime.send) != 0 {
				t.Fatalf("reply=%+v", reply)
			}
			encoded, _ := json.Marshal(reply)
			if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "token") {
				t.Fatal("raw failure leaked")
			}
		})
	}
	for _, field := range []string{"local_skill_id", "workspace_generation"} {
		t.Run("replay changed "+field, func(t *testing.T) {
			f, s, _ := newPublicationIntakeFixture(t)
			f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
			if r := publicationIntakeResponse(t, f.owner); r.Error != nil {
				t.Fatal(r.Error)
			}
			r := publicationIntakeRPC()
			r.Payload[field] = "another-source"
			f.h.handleThinToolRPCRequest(f.owner, r)
			if r := publicationIntakeResponse(t, f.owner); r.Error == nil || r.Error.Code != "publication.request_conflict" {
				t.Fatalf("reply=%+v", r)
			}
			if s.creates != 1 || len(s.entries) != 1 {
				t.Fatal("conflict created another entry")
			}
		})
	}
}

func TestSkillHubPublicationWorkspaceDurableDisconnectDuringCommit(t *testing.T) {
	f, s, w := newPublicationIntakeFixture(t)
	s.onCreate = func() { f.h.removeClient(f.runtime) }
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	reply := publicationIntakeResponse(t, f.owner)
	if reply.Error != nil || len(s.entries) != 1 {
		t.Fatalf("committed admission incorrectly rejected on runtime loss: %+v", reply)
	}
	w.sweep()
	if s.claims != 0 || len(f.runtime.send) != 0 {
		t.Fatal("offline runtime consumed attempts")
	}
	h := NewSkillHubPublicationReadHandler(s, true)
	runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/requests/click-0001", 7), 200)
}

func TestSkillHubPublicationWorkspaceDurableConcurrentBoundAndPrivacy(t *testing.T) {
	f, s, _ := newPublicationIntakeFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.onCreate = func() { close(entered); <-release }
	f.h.publicationAdmissions = make(chan struct{}, 1)
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not start")
	}
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	if r := publicationIntakeResponse(t, f.owner); r.Error == nil || r.Error.Code != "publication.busy" {
		t.Fatalf("reply=%+v", r)
	}
	// A replacement owner login must not receive the old socket's response.
	f.h.removeClient(f.owner)
	next := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	f.h.addClient(next)
	unblock()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.h.publicationAdmissions) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(f.h.publicationAdmissions) != 0 || len(next.send) != 0 || len(f.owner.send) != 0 {
		t.Fatal("old private reply leaked or admission stuck")
	}
	h := NewSkillHubPublicationReadHandler(s, true)
	runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/requests/click-0001", 7), http.StatusOK)
}

type publicationIntakeFailureStore struct {
	*publicationDeliveryTestStore
	readErr, afterCommitErr error
}

func (s *publicationIntakeFailureStore) GetSkillHubPublicationEntryByRequest(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationEntry, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.publicationDeliveryTestStore.GetSkillHubPublicationEntryByRequest(ctx, actor, id)
}

func (s *publicationIntakeFailureStore) CreateSkillHubPublicationEntry(ctx context.Context, in store.SkillHubPublicationEntryInput, policy store.SkillHubPublicationEntryPolicy) (*store.SkillHubPublicationEntry, bool, error) {
	e, replay, err := s.publicationDeliveryTestStore.CreateSkillHubPublicationEntry(ctx, in, policy)
	if err == nil && s.afterCommitErr != nil {
		return nil, false, s.afterCommitErr
	}
	return e, replay, err
}

func TestSkillHubPublicationWorkspaceDurableUncertainCommitAndLookup(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(strconv.FormatBool(commit), func(t *testing.T) {
			var backend *publicationIntakeFailureStore
			f, s, _ := newPublicationIntakeFixtureWithStore(t, func(s *publicationDeliveryTestStore) store.Store {
				backend = &publicationIntakeFailureStore{publicationDeliveryTestStore: s}
				if commit {
					backend.afterCommitErr = errors.New("secret commit connection lost")
				} else {
					backend.readErr = errors.New("secret read connection lost")
				}
				return backend
			})
			f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
			reply := publicationIntakeResponse(t, f.owner)
			if reply.Error == nil || reply.Error.Code != "publication.admission_uncertain" || len(f.runtime.send) != 0 {
				t.Fatalf("reply=%+v", reply)
			}
			if commit {
				// Lookup/retry keeps the original ID; no new event or auto-probe.
				h := NewSkillHubPublicationReadHandler(s, true)
				runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/requests/click-0001", 7), 200)
				backend.afterCommitErr = nil
				f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
				r := publicationIntakeResponse(t, f.owner)
				if r.Error != nil || r.Result.(map[string]interface{})["replay"] != true || len(s.entries) != 1 {
					t.Fatalf("recovery=%+v", r)
				}
			} else if s.creates != 0 {
				t.Fatal("lookup uncertainty was treated as a missing entry")
			}
		})
	}
}

func TestSkillHubPublicationWorkspaceDurableDuplicateAndTransfer(t *testing.T) {
	f, s, w := newPublicationIntakeFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC()) }()
	}
	wg.Wait()
	replays := 0
	for i := 0; i < 2; i++ {
		r := publicationIntakeResponse(t, f.owner)
		if r.Error != nil {
			t.Fatal(r.Error)
		}
		if r.Result.(map[string]interface{})["replay"] == true {
			replays++
		}
	}
	if replays != 1 || len(s.entries) != 1 {
		t.Fatal("concurrent duplicate intake was not idempotent")
	}
	s.config.OwnerID = 9
	w.sweep()
	if s.claims != 0 || len(f.runtime.send) != 0 {
		t.Fatal("transferred Bot still probed under old authority")
	}
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	if r := publicationIntakeResponse(t, f.owner); r.Error == nil || r.Error.Code != "publication.owner_required" {
		t.Fatalf("replay after transfer=%+v", r)
	}
	// Historical read remains available to original publisher, not the new owner.
	s.users[9] = &types.User{ID: 9, AccountType: types.AccountHuman}
	h := NewSkillHubPublicationReadHandler(s, true)
	runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/requests/click-0001", 7), 200)
	runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/requests/click-0001", 9), 404)
}

func TestSkillHubPublicationWorkspaceDurableCapabilities(t *testing.T) {
	f, s, _ := newPublicationIntakeFixture(t)
	for _, delivery := range []string{"", "false", "1"} {
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", "true")
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED", "true")
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DELIVERY_ENABLED", delivery)
		for _, backend := range []store.Store{s, f.db} {
			h := NewSkillHubPublicationReadHandlerFromEnv(backend)
			rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 7), 200)
			var caps map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
				t.Fatal(err)
			}
			want := delivery == "1" && backend == s
			if caps["workspaceDurableCreateEnabled"] != want || caps["workspaceCreateEnabled"] != true || caps["chatCreateEnabled"] != false {
				t.Fatalf("caps=%v", caps)
			}
		}
	}
}

func TestSkillHubPublicationWorkspaceDurableSourceFailurePersists(t *testing.T) {
	f, s, w := newPublicationIntakeFixture(t)
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	if r := publicationIntakeResponse(t, f.owner); r.Error != nil {
		t.Fatal(r.Error)
	}
	w.sweep()
	probe := deliveryProbe(t, f.runtime)
	f.h.handleThinToolRPCResult(f.runtime, &MsgThinToolRPC{Type: thinToolRPCTypeResult, RequestID: probe.RequestID,
		Error: &MsgDeviceRPCError{Code: "publication.source_missing", Message: "private /root/secret"}})
	deliveryProcess(t, w)
	deliveryAckStatus(t, f.runtime, 200)
	j := s.jobs[publicationReadTestID]
	if j.state != "needs_user_action" || j.code != "publication.source_missing" || len(s.entries) != 1 {
		t.Fatal("source failure erased intent or was misclassified")
	}
	h := NewSkillHubPublicationReadHandler(s, true)
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/entries/"+publicationReadTestID+"/source-status", 7), 200)
	if !strings.Contains(rec.Body.String(), `"stage":"needs_user_action"`) || !strings.Contains(rec.Body.String(), "publication.source_missing") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("failure not privately readable: %s", rec.Body.String())
	}
	f.h.handleThinToolRPCRequest(f.owner, publicationIntakeRPC())
	if r := publicationIntakeResponse(t, f.owner); r.Error != nil || r.Result.(map[string]interface{})["replay"] != true {
		t.Fatalf("reply=%+v", r)
	}
	w.sweep()
	if s.claims != 1 || len(f.runtime.send) != 0 {
		t.Fatal("replay revived terminal source failure")
	}
}

func TestSkillHubPublicationWorkspaceDurableReplyBackpressureAndIdentity(t *testing.T) {
	f, _, _ := newPublicationIntakeFixture(t)
	r := publicationIntakeRequest{actor: 7, client: f.owner, route: f.h.clientRoute(f.owner), requestID: "click-0001"}
	for len(f.owner.send) < cap(f.owner.send) {
		f.owner.send <- []byte("occupied")
	}
	f.h.publicationIntakeReply(r, map[string]any{"state": "waiting_runtime"}, "")
	if !f.h.isClientRegistered(f.owner) {
		t.Fatal("reply backpressure disconnected Owner")
	}
	for len(f.owner.send) > 0 {
		<-f.owner.send
	}
	f.h.removeClient(f.owner)
	f.owner.uid = 8
	f.h.addClient(f.owner)
	f.h.publicationIntakeReply(r, map[string]any{"state": "waiting_runtime"}, "")
	if len(f.owner.send) != 0 {
		t.Fatal("reply leaked across same-socket identity change")
	}
}
