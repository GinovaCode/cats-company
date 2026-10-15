package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type publicationWorkspaceTestStore struct {
	store.Store
	users                             map[int64]*types.User
	config                            types.BotConfig
	generation                        uint64
	generationErr, createErr, userErr error
	mu                                sync.Mutex
	entries                           map[string]*store.SkillHubPublicationEntry
	creates                           int
	onCreate                          func()
}

func (s *publicationWorkspaceTestStore) GetUser(uid int64) (*types.User, error) {
	return s.users[uid], s.userErr
}
func (s *publicationWorkspaceTestStore) GetBotConfig(uid int64) (*types.BotConfig, error) {
	c := s.config
	return &c, nil
}
func (s *publicationWorkspaceTestStore) BotConnectionGeneration(uid int64) (uint64, error) {
	return s.generation, s.generationErr
}
func (s *publicationWorkspaceTestStore) BumpBotConnectionGeneration(uid int64) (uint64, error) {
	panic("entry must not bump/rebind runtime generation")
}
func (s *publicationWorkspaceTestStore) GetSkillHubPublicationEntry(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry.ID == id && entry.Input.ActorUID == actor {
			return entry, nil
		}
	}
	return nil, nil
}

func (s *publicationWorkspaceTestStore) GetSkillHubPublicationEntryByRequest(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entries[id]; e != nil && e.Input.ActorUID == actor {
		return e, nil
	}
	return nil, nil
}
func (s *publicationWorkspaceTestStore) CreateSkillHubPublicationEntry(ctx context.Context, in store.SkillHubPublicationEntryInput, p store.SkillHubPublicationEntryPolicy) (*store.SkillHubPublicationEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creates++
	if s.createErr != nil {
		return nil, false, s.createErr
	}
	if in.Validate() != nil || p.Validate() != nil {
		return nil, false, store.ErrSkillHubPublicationInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, false, errors.New("missing deadline")
	}
	if s.onCreate != nil {
		s.onCreate()
	}
	if old := s.entries[in.RequestID]; old != nil {
		if old.Input != in {
			return nil, false, store.ErrSkillHubPublicationConflict
		}
		return old, true, nil
	}
	entry := &store.SkillHubPublicationEntry{ID: publicationReadTestID, Input: in, State: "waiting_runtime", CreatedAt: time.Now()}
	s.entries[in.RequestID] = entry
	return entry, false, nil
}

type publicationWorkspaceFixture struct {
	h              *Hub
	db             *publicationWorkspaceTestStore
	owner, runtime *Client
	device         UserDevice
}

func newPublicationWorkspaceFixture(t *testing.T) *publicationWorkspaceFixture {
	return newPublicationWorkspaceFixtureWithStore(t, nil)
}

func newPublicationWorkspaceFixtureWithStore(t *testing.T, wrap func(*publicationWorkspaceTestStore) store.Store) *publicationWorkspaceFixture {
	t.Helper()
	db := &publicationWorkspaceTestStore{
		users:  map[int64]*types.User{7: {ID: 7, AccountType: types.AccountHuman}, 42: {ID: 42, AccountType: types.AccountBot}},
		config: types.BotConfig{UserID: 42, OwnerID: 7, Enabled: true, BodyID: "body-42"}, generation: 1,
		entries: map[string]*store.SkillHubPublicationEntry{},
	}
	var backend store.Store = db
	if wrap != nil {
		backend = wrap(db)
	}
	h := NewHub(backend, nil)
	h.publicationWorkspaceEntries = true
	owner := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 32)}
	runtime := &Client{uid: 42, accountType: types.AccountBot, bodyID: "body-42", installationID: "install-42", send: make(chan []byte, 32)}
	h.addClient(owner)
	h.addClient(runtime)
	if _, err := h.bodyLeases.acquire(42, "body-42", runtime.connectionID); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.botConnectionEpochs[42] = 1
	h.mu.Unlock()
	device, err := h.userDevices.register(7, RegisterUserDeviceRequest{BotUID: 42, DeviceID: "runtime-42", BodyID: "body-42", InstallationID: "install-42", RuntimeRole: "server", Status: "online", Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe)}})
	if err != nil {
		t.Fatal(err)
	}
	h.bindDeviceClient(7, device, runtime)
	return &publicationWorkspaceFixture{h: h, db: db, owner: owner, runtime: runtime, device: device}
}

func publicationWorkspaceRequest() *MsgThinToolRPC {
	return &MsgThinToolRPC{ID: "ui-action", Type: thinToolRPCTypeRequest, RequestID: "click-0001", TargetOwnerUserID: "usr7", TargetDeviceID: "runtime-42", ToolName: publicationWorkspaceCreate,
		Payload: map[string]interface{}{"bot_uid": "42", "local_skill_id": "local-skill-1", "workspace_generation": "workspace-v1"},
	}
}

func (f *publicationWorkspaceFixture) start(t *testing.T, req *MsgThinToolRPC) *MsgThinToolRPC {
	t.Helper()
	f.h.handleThinToolRPCRequest(f.owner, req)
	var forwarded, ack ServerMessage
	decodeQueuedServerMessage(t, f.runtime.send, &forwarded)
	decodeQueuedServerMessage(t, f.owner.send, &ack)
	if forwarded.ThinToolRPC == nil || ack.Ctrl == nil || ack.Ctrl.Code != 200 {
		t.Fatalf("forward=%+v ack=%+v", forwarded, ack)
	}
	if forwarded.ThinToolRPC.ToolName != string(DeviceCapabilitySkillHubPublicationProbe) || forwarded.ThinToolRPC.RequestID == req.RequestID {
		t.Fatal("probe not server attributed")
	}
	return forwarded.ThinToolRPC
}

func (f *publicationWorkspaceFixture) result(probe *MsgThinToolRPC) *MsgThinToolRPC {
	return &MsgThinToolRPC{ID: "runtime-result", Type: thinToolRPCTypeResult, RequestID: probe.RequestID, Result: map[string]interface{}{
		"schema_version": float64(1), "local_skill_id": probe.Payload["local_skill_id"], "workspace_generation": probe.Payload["workspace_generation"],
	}}
}

func publicationWorkspaceResponse(t *testing.T, c *Client) *MsgThinToolRPC {
	t.Helper()
	var msg ServerMessage
	decodeQueuedServerMessage(t, c.send, &msg)
	if msg.ThinToolRPC == nil {
		t.Fatalf("expected result, got %+v", msg)
	}
	return msg.ThinToolRPC
}

func TestSkillHubPublicationWorkspaceAdmissionAndReplay(t *testing.T) {
	f := newPublicationWorkspaceFixture(t)
	for i := 0; i < 2; i++ {
		probe := f.start(t, publicationWorkspaceRequest())
		if f.db.creates != i {
			t.Fatal("intent saved before runtime verification")
		}
		f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
		reply := publicationWorkspaceResponse(t, f.owner)
		if reply.Error != nil || reply.RequestID != "click-0001" || reply.ToolName != publicationWorkspaceCreate {
			t.Fatalf("reply=%+v", reply)
		}
		result := reply.Result.(map[string]interface{})
		if len(result) != 4 || result["state"] != "waiting_runtime" || result["entry_request_id"] != publicationReadTestID || result["replay"] != (i == 1) {
			t.Fatalf("result=%v", result)
		}
		var ack ServerMessage
		decodeQueuedServerMessage(t, f.runtime.send, &ack)
		if ack.Ctrl == nil || ack.Ctrl.Code != 200 {
			t.Fatalf("ack=%+v", ack)
		}
	}
	if len(f.db.entries) != 1 {
		t.Fatal("duplicate durable entry")
	}
	in := f.db.entries["click-0001"].Input
	if in.ActorUID != 7 || in.BotUID != 42 || in.Source.BodyID != "body-42" || in.Source.InstallationID != "install-42" || in.Source.RouteGeneration != 1 || in.Source.DeviceID != "runtime-42" {
		t.Fatalf("identity=%+v", in)
	}
	if f.db.config.BodyID != "body-42" || f.db.generation != 1 {
		t.Fatal("runtime was rebound")
	}
}

func TestSkillHubPublicationWorkspaceRequestGuards(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*publicationWorkspaceFixture, *MsgThinToolRPC)
	}{
		{"off", "publication.disabled", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.h.publicationWorkspaceEntries = false }},
		{"raw probe", "publication.disabled", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			r.ToolName = string(DeviceCapabilitySkillHubPublicationProbe)
		}},
		{"bot initiator", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.owner.accountType = types.AccountBot }},
		{"wiki owner", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.owner.wikiAgentUID = 42 }},
		{"connector", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			f.owner.deviceConnector = &DeviceConnectorClaims{}
		}},
		{"friend", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.config.OwnerID = 8 }},
		{"disabled owner", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.users[7].State = 1 }},
		{"deleted owner", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { delete(f.db.users, 7) }},
		{"disabled bot", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.users[42].State = 1 }},
		{"disabled config", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.config.Enabled = false }},
		{"injected identity", "publication.invalid_request", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { r.Payload["actor_uid"] = "7" }},
		{"numeric bot", "publication.invalid_request", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { r.Payload["bot_uid"] = float64(42) }},
		{"wrong owner header", "publication.invalid_request", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { r.TargetOwnerUserID = "usr8" }},
		{"path", "publication.invalid_request", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			r.Payload["local_skill_id"] = "/root/private/SKILL.md"
		}},
		{"expired action", "publication.invalid_request", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			r.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
		}},
		{"unbound body", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.config.BodyID = "" }},
		{"legacy body", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.config.BodyID = "legacy:42" }},
		{"other body", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.config.BodyID = "body-elsewhere" }},
		{"wrong socket bot", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.runtime.uid = 43 }},
		{"socket install mismatch", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.runtime.installationID = "other-install" }},
		{"expired lease", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			f.h.bodyLeases.mu.Lock()
			lease := f.h.bodyLeases.leases[42]
			lease.expiresAt = time.Now().Add(-time.Second)
			f.h.bodyLeases.leases[42] = lease
			f.h.bodyLeases.mu.Unlock()
		}},
		{"unknown generation", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.generation = 0 }},
		{"new generation", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.generation = 2 }},
		{"generation lookup failed", "publication.authority_unavailable", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			f.db.generationErr = errors.New("secret database error")
		}},
		{"account lookup failed", "publication.authority_unavailable", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			f.db.userErr = errors.New("secret database error")
		}},
		{"old runtime", "publication.runtime_unsupported", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			f.h.userDevices.mu.Lock()
			d := f.h.userDevices.devices[7]["runtime-42"]
			d.Capabilities = []DeviceGrantOperation{DeviceGrantSkillHubSkillShare}
			f.h.userDevices.devices[7]["runtime-42"] = d
			f.h.userDevices.mu.Unlock()
		}},
		{"device offline", "publication.runtime_unavailable", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.h.userDevices.unregister(7, "runtime-42") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublicationWorkspaceFixture(t)
			req := publicationWorkspaceRequest()
			tc.change(f, req)
			f.h.handleThinToolRPCRequest(f.owner, req)
			reply := publicationWorkspaceResponse(t, f.owner)
			if reply.Error == nil || reply.Error.Code != tc.code {
				t.Fatalf("reply=%+v", reply)
			}
			if f.db.creates != 0 || len(f.runtime.send) != 0 {
				t.Fatal("denied request dispatched or persisted")
			}
		})
	}
}

func TestSkillHubPublicationWorkspaceStaleAndPrivateResults(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*publicationWorkspaceFixture, *MsgThinToolRPC)
	}{
		{"source changed", "publication.source_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			r.Result.(map[string]interface{})["workspace_generation"] = "new-generation"
		}},
		{"untrusted fields", "publication.source_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			r.Result.(map[string]interface{})["path"] = "/root/secret"
		}},
		{"raw runtime error", "publication.source_unavailable", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			r.Error = &MsgDeviceRPCError{Code: "arbitrary-secret", Message: "/root/secret Bearer password"}
		}},
		{"safe runtime error", "publication.source_unsafe", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			r.Error = &MsgDeviceRPCError{Code: "publication.source_unsafe", Message: "/root/secret"}
		}},
		{"owner transferred", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.config.OwnerID = 8 }},
		{"owner disabled", "publication.owner_required", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.users[7].State = 1 }},
		{"bot rebound", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.config.BodyID = "new-body" }},
		{"generation changed", "publication.runtime_changed", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) { f.db.generation = 2 }},
		{"capability revoked", "publication.runtime_unsupported", func(f *publicationWorkspaceFixture, r *MsgThinToolRPC) {
			f.h.userDevices.mu.Lock()
			d := f.h.userDevices.devices[7]["runtime-42"]
			d.Capabilities = nil
			f.h.userDevices.devices[7]["runtime-42"] = d
			f.h.userDevices.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublicationWorkspaceFixture(t)
			probe := f.start(t, publicationWorkspaceRequest())
			result := f.result(probe)
			tc.change(f, result)
			f.h.handleThinToolRPCResult(f.runtime, result)
			reply := publicationWorkspaceResponse(t, f.owner)
			if reply.Error == nil || reply.Error.Code != tc.code || f.db.creates != 0 {
				t.Fatalf("reply=%+v creates=%d", reply, f.db.creates)
			}
			encoded, _ := json.Marshal(reply)
			if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "password") {
				t.Fatal("raw private response exposed")
			}
		})
	}
}

func TestSkillHubPublicationWorkspaceWrongSocketCannotConsumeProbe(t *testing.T) {
	f := newPublicationWorkspaceFixture(t)
	probe := f.start(t, publicationWorkspaceRequest())
	wrong := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
	f.h.addClient(wrong)
	f.h.handleThinToolRPCResult(wrong, f.result(probe))
	var ack ServerMessage
	decodeQueuedServerMessage(t, wrong.send, &ack)
	if ack.Ctrl == nil || ack.Ctrl.Code != 403 {
		t.Fatalf("ack=%+v", ack)
	}
	if _, ok := f.h.thinToolRPC.get(probe.RequestID); !ok {
		t.Fatal("wrong socket consumed probe")
	}
	f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
	if reply := publicationWorkspaceResponse(t, f.owner); reply.Error != nil {
		t.Fatalf("reply=%+v", reply)
	}
	if f.db.creates != 1 {
		t.Fatal("expected one entry")
	}
}

func TestSkillHubPublicationWorkspaceTimeoutReplacementAndDisconnect(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		f := newPublicationWorkspaceFixture(t)
		probe := f.start(t, publicationWorkspaceRequest())
		if f.h.expireThinToolRPCRequests(time.UnixMilli(probe.ExpiresAt).Add(time.Second)) != 1 {
			t.Fatal("not expired")
		}
		reply := publicationWorkspaceResponse(t, f.owner)
		if reply.RequestID != "click-0001" || reply.Error.Code != "publication.runtime_timeout" || f.db.creates != 0 {
			t.Fatalf("reply=%+v", reply)
		}
	})
	t.Run("replace", func(t *testing.T) {
		f := newPublicationWorkspaceFixture(t)
		probe := f.start(t, publicationWorkspaceRequest())
		next := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 4)}
		f.h.addClient(next)
		f.h.bindDeviceClient(7, f.device, next)
		reply := publicationWorkspaceResponse(t, f.owner)
		if reply.RequestID != "click-0001" || reply.Error.Code != "publication.runtime_changed" {
			t.Fatalf("reply=%+v", reply)
		}
		f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
		if f.db.creates != 0 {
			t.Fatal("late result persisted")
		}
	})
	t.Run("requester disconnect", func(t *testing.T) {
		f := newPublicationWorkspaceFixture(t)
		probe := f.start(t, publicationWorkspaceRequest())
		f.h.mu.Lock()
		delete(f.h.clients[7], f.owner)
		delete(f.h.clientsByConn, f.owner.connectionID)
		f.h.mu.Unlock()
		f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
		if len(f.owner.send) != 0 || f.db.creates != 0 {
			t.Fatal("disconnected requester received private result or started admission")
		}
	})
}

func TestSkillHubPublicationWorkspaceAdmissionErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{store.ErrSkillHubPublicationDenied, "publication.owner_required"},
		{store.ErrSkillHubPublicationConflict, "publication.request_conflict"},
		{store.ErrSkillHubPublicationCapacity, "publication.capacity"},
		{store.ErrSkillHubPublicationRateLimit, "publication.rate_limited"},
		{errors.New("password at /root/private"), "publication.admission_uncertain"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newPublicationWorkspaceFixture(t)
			f.db.createErr = tc.err
			probe := f.start(t, publicationWorkspaceRequest())
			f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
			reply := publicationWorkspaceResponse(t, f.owner)
			if reply.Error == nil || reply.Error.Code != tc.code {
				t.Fatalf("reply=%+v", reply)
			}
			if strings.Contains(reply.Error.Message, "password") {
				t.Fatal("DB error leaked")
			}
		})
	}
	t.Run("transfer during admission", func(t *testing.T) {
		f := newPublicationWorkspaceFixture(t)
		f.db.onCreate = func() { f.db.config.OwnerID = 8 }
		probe := f.start(t, publicationWorkspaceRequest())
		f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
		reply := publicationWorkspaceResponse(t, f.owner)
		if reply.Error == nil || reply.Error.Code != "publication.admission_uncertain" || len(f.db.entries) != 1 {
			t.Fatalf("reply=%+v", reply)
		}
	})
}

func TestSkillHubPublicationWorkspaceConcurrentDuplicateReceipt(t *testing.T) {
	f := newPublicationWorkspaceFixture(t)
	probe := f.start(t, publicationWorkspaceRequest())
	result := f.result(probe)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.h.handleThinToolRPCResult(f.runtime, result) }()
	}
	wg.Wait()
	if f.db.creates != 1 || len(f.db.entries) != 1 {
		t.Fatalf("creates=%d", f.db.creates)
	}
	if reply := publicationWorkspaceResponse(t, f.owner); reply.Error != nil {
		t.Fatalf("reply=%+v", reply)
	}
	if len(f.owner.send) != 0 {
		t.Fatal("duplicate private result")
	}
}

func TestSkillHubPublicationWorkspaceFlagsAndLimits(t *testing.T) {
	for _, tc := range []struct {
		read, create string
		enabled      bool
	}{{"", "", false}, {"", "true", false}, {"true", "", false}, {"true", "1", true}} {
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", tc.read)
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED", tc.create)
		if publicationWorkspaceEntriesEnabled() != tc.enabled {
			t.Fatal("incorrect flag combination")
		}
		f := newPublicationWorkspaceFixture(t)
		h := NewSkillHubPublicationReadHandlerFromEnv(f.db)
		rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 7), http.StatusOK)
		var caps map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
			t.Fatal(err)
		}
		if caps["workspaceCreateEnabled"] != tc.enabled || caps["chatCreateEnabled"] != false {
			t.Fatalf("caps=%v", caps)
		}
	}
	f := newPublicationWorkspaceFixture(t)
	for i := 0; i < 3; i++ {
		f.start(t, publicationWorkspaceRequest())
	}
	f.h.handleThinToolRPCRequest(f.owner, publicationWorkspaceRequest())
	if reply := publicationWorkspaceResponse(t, f.owner); reply.Error == nil || reply.Error.Code != "publication.rate_limited" {
		t.Fatalf("reply=%+v", reply)
	}
}

func TestSkillHubPublicationWorkspaceRecoveryRead(t *testing.T) {
	f := newPublicationWorkspaceFixture(t)
	probe := f.start(t, publicationWorkspaceRequest())
	f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
	// Browser discards the response; then runtime reconnects and Bot transfers.
	<-f.owner.send
	f.db.generation = 2
	f.db.config.OwnerID = 8
	h := NewSkillHubPublicationReadHandler(f.db, true)
	path := skillHubPublicationPrefix + "/requests/click-0001"
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 7), 200)
	if !strings.Contains(rec.Body.String(), publicationReadTestID) || strings.Contains(rec.Body.String(), "click-0001") || strings.Contains(rec.Body.String(), "install-42") {
		t.Fatalf("unsafe recovery: %s", rec.Body.String())
	}
	f.db.users[8] = &types.User{ID: 8, AccountType: types.AccountHuman}
	runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 8), 404)
	runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/requests/not-found-key", 7), 404)
	runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/requests/short", 7), 404)
	runPublicationRead(t, h, publicationReadRequest(t, "POST", path, 7), 405)
	f.db.users[7].State = 1
	runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 7), 403)
}

func TestSkillHubPublicationWorkspaceLocalAndSharedRoutes(t *testing.T) {
	t.Run("bound desktop", func(t *testing.T) {
		f := newPublicationWorkspaceFixture(t)
		f.h.userDevices.mu.Lock()
		d := f.h.userDevices.devices[7]["runtime-42"]
		d.RuntimeRole = "desktop"
		f.h.userDevices.devices[7]["runtime-42"] = d
		f.h.userDevices.mu.Unlock()
		probe := f.start(t, publicationWorkspaceRequest())
		f.h.handleThinToolRPCResult(f.runtime, f.result(probe))
		if reply := publicationWorkspaceResponse(t, f.owner); reply.Error != nil {
			t.Fatalf("reply=%+v", reply)
		}
	})
	t.Run("cross-node fails closed", func(t *testing.T) {
		f := newPublicationWorkspaceFixture(t)
		shared := newSharedMemoryRuntimeState()
		f.h.sharedRuntime = shared
		remote := runtimeRoute{NodeID: "another-node", ConnectionID: "remote-connection", ExpiresAt: time.Now().Add(time.Minute)}
		shared.bindUserDeviceRoute(7, f.device, remote, time.Now())
		f.h.handleThinToolRPCRequest(f.owner, publicationWorkspaceRequest())
		reply := publicationWorkspaceResponse(t, f.owner)
		if reply.Error == nil || reply.Error.Code != "publication.route_unsupported" || f.db.creates != 0 {
			t.Fatalf("reply=%+v", reply)
		}
	})
	t.Run("bare owner desktop", func(t *testing.T) {
		f := newPublicationWorkspaceFixture(t)
		f.h.userDevices.mu.Lock()
		d := f.h.userDevices.devices[7]["runtime-42"]
		d.RuntimeRole = "desktop"
		d.BotUID = 0
		f.h.userDevices.devices[7]["runtime-42"] = d
		f.h.userDevices.mu.Unlock()
		f.h.handleThinToolRPCRequest(f.owner, publicationWorkspaceRequest())
		reply := publicationWorkspaceResponse(t, f.owner)
		if reply.Error == nil || reply.Error.Code != "publication.runtime_changed" {
			t.Fatalf("reply=%+v", reply)
		}
	})
}
