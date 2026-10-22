package server

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestXiaobaRuntimeMetadataCannotBeForgedByInboundClients(t *testing.T) {
	input := map[string]interface{}{
		"xiaoba_runtime": map[string]interface{}{"schema": "xiaoba.runtime.v2", "devices": []string{"forged"}},
		"keep":           "visible",
	}
	clean := metadataWithoutXiaobaRuntime(input)
	if reflect.DeepEqual(clean, input) || clean["xiaoba_runtime"] != nil || clean["keep"] != "visible" {
		t.Fatalf("runtime metadata was not stripped: %#v", clean)
	}
	if got := withXiaobaRuntimeMetadata(input, nil); got["xiaoba_runtime"] != nil || got["keep"] != "visible" {
		t.Fatalf("nil server runtime did not remove stale client value: %#v", got)
	}
	serverRuntime := map[string]interface{}{"schema": "xiaoba.runtime.v1"}
	got := withXiaobaRuntimeMetadata(input, serverRuntime)
	if !reflect.DeepEqual(got["xiaoba_runtime"], serverRuntime) {
		t.Fatalf("server runtime was not authoritative: %#v", got)
	}
}

// The helper-level test above does not prove the real paths strip the key. A
// forged rollout signal must not survive ingestion, persistence, or replay.
func TestForgedRuntimeMetadataCannotReachPersistenceOrReplay(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "0")
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_BOT_UIDS", "42") // bot 43 is not listed

	forged := map[string]interface{}{
		"keep": "visible",
		"xiaoba_runtime": map[string]interface{}{
			"schema": "xiaoba.runtime.v2", "devices": []interface{}{"forged"},
		},
	}

	// Ingestion choke point shared by HTTP, WebSocket and channel paths.
	payload, err := normalizeMessageRequest(&SendMessageRequest{
		TopicID: "p2p_7_42", Content: json.RawMessage(`"hi"`), Metadata: forged,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := payload.Metadata["xiaoba_runtime"]; present {
		t.Fatalf("ingestion kept a forged directory: %#v", payload.Metadata)
	}
	if payload.Metadata["keep"] != "visible" {
		t.Fatalf("ingestion dropped unrelated metadata: %#v", payload.Metadata)
	}

	// Persistence boundary.
	store := &metadataMessageStore{}
	if _, err := saveNormalizedMessage(store, "p2p_7_42", 7, 0, payload); err != nil {
		t.Fatal(err)
	}
	if _, present := store.metadata["xiaoba_runtime"]; present {
		t.Fatalf("persistence kept a forged directory: %#v", store.metadata)
	}
	if store.metadata["keep"] != "visible" {
		t.Fatalf("persistence dropped unrelated metadata: %#v", store.metadata)
	}

	// History replay, including rows written before the key was scrubbed.
	hub := NewHub(&identityMessageStore{users: map[int64]*types.User{
		7:  {ID: 7, Username: "alice", AccountType: types.AccountHuman},
		42: {ID: 42, Username: "cloud", AccountType: types.AccountBot},
	}}, nil)
	replay := hub.historyMessageDataForRecipient(42, &types.Message{
		ID: 1, TopicID: "p2p_7_42", FromUID: 7, Content: `"hi"`,
		Metadata: map[string]interface{}{"xiaoba_runtime": map[string]interface{}{"schema": "xiaoba.runtime.v2"}},
	})
	if replay == nil {
		t.Fatal("expected replayed message")
	}
	if _, present := replay.Metadata["xiaoba_runtime"]; present {
		t.Fatalf("history replay kept a forged directory: %#v", replay.Metadata)
	}
}

func TestRuntimeV2BotCanaryConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, global, list string
		uid                int64
		want               bool
	}{
		{"default off", "", "", 42, false},
		{"canary", "0", "42", 42, true},
		{"multiple canaries", "", " 42, 43 ", 43, true},
		{"unlisted bot", "", "42", 43, false},
		{"no substring match", "", "142,420", 42, false},
		{"no aliases or wildcard", "", "usr42,David,*,+42,042,4.2e1", 42, false},
		{"invalid uid", "", "0,-1", 0, false},
		{"global opt in", "1", "42", 43, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", tc.global)
			t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_BOT_UIDS", tc.list)
			if got := xiaobaRuntimeV2EnabledForBot(tc.uid); got != tc.want {
				t.Fatalf("enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRuntimeV2BotCanarySeparatesMetadataAndCapabilities(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "0")
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_BOT_UIDS", "42")
	hub, _ := deviceSelectionFixture(t)
	for _, uid := range []int64{42, 43} {
		hub.addClient(&Client{uid: uid, accountType: types.AccountBot, bodyID: "cloud", send: make(chan []byte, 8)})
		session := buildCatsCoSessionKey("grp_1", "group", formatUID(uid), 7)
		if _, err := hub.userDevices.confirmDeviceSelection(7, session, "a", "test", ""); err != nil {
			t.Fatal(err)
		}
		metadata := hub.buildXiaobaRuntimeMetadata(7, uid, "grp_1")
		wantSchema, wantDevices := "xiaoba.runtime.v1", 2
		if uid == 42 {
			wantSchema, wantDevices = "xiaoba.runtime.v2", 3
		}
		if metadata["schema"] != wantSchema || len(metadata["devices"].([]map[string]interface{})) != wantDevices {
			t.Fatalf("bot %d: unexpected runtime metadata %#v", uid, metadata)
		}
		identity := hub.buildCatscoIdentityMetadata(7, uid, "grp_1", 1, "operate b")
		legacy, _ := identity["device_grants"].([]ScopedDeviceGrant)
		if len(legacy) != 1 || legacy[0].DeviceID != "a" || legacy[0].TargetScope != "" {
			t.Fatalf("bot %d: legacy default widened: %#v", uid, legacy)
		}
		explicit, hasExplicit := identity["device_target_authorizations"]
		if hasExplicit != (uid == 42) {
			t.Fatalf("bot %d: explicit envelope escaped the canary gate", uid)
		}
		if hasExplicit {
			grants := explicit.(map[string]interface{})["grants"].([]ScopedDeviceGrant)
			if len(grants) != 2 {
				t.Fatalf("expected two own online devices, got %#v", grants)
			}
			for _, grant := range grants {
				if grant.OwnerUserID != "usr7" || grant.AgentID != "usr42" || grant.TargetScope != "explicit_device" {
					t.Fatalf("unexpected capability scope: %#v", grant)
				}
			}
		}
	}
}

func TestRuntimeV2BotCanaryRemovalRejectsIssuedCapabilities(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "0")
	for _, backend := range []string{"memory", "shared", "redis", "durable"} {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_BOT_UIDS", "42,43")
			r := newUserDeviceRegistry(time.Hour)
			switch backend {
			case "shared":
				r.withSharedRuntime(newSharedMemoryRuntimeState())
			case "durable":
				r.withSelectionStore(&durableSelectionTestStore{})
			case "redis":
				url, closeRedis := newRedisRuntimeTestServer(t)
				defer closeRedis()
				state := newRedisRuntimeStateForTest(t, url, t.Name())
				defer state.Close()
				r.withSharedRuntime(state)
			}
			devices := []UserDevice{
				{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
				{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
			}
			contexts := map[int64]DeviceTurnContext{}
			for _, uid := range []int64{42, 43} {
				session := buildCatsCoSessionKey("grp_1", "group", formatUID(uid), 7)
				if _, err := r.confirmDeviceSelection(7, session, "a", "test", ""); err != nil {
					t.Fatal(err)
				}
				contexts[uid] = r.turnContextForDevices(7, "grp_1", "group", uid, "cloud", devices, nil)
				grant := deviceGrantForID(t, contexts[uid].ExplicitGrants, "b")
				if _, ok := r.lookupGrant(grant.GrantID); !ok {
					t.Fatal("fresh canary grant rejected")
				}
			}
			t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_BOT_UIDS", "43")
			for uid, ctx := range contexts {
				grant := deviceGrantForID(t, ctx.ExplicitGrants, "b")
				if _, ok := r.lookupGrant(grant.GrantID); ok != (uid == 43) {
					t.Fatalf("bot %d: capability lookup did not honor removal", uid)
				}
				if _, ok := r.lookupGrant(ctx.Grants[0].GrantID); !ok {
					t.Fatal("removing v2 broke the legacy default grant")
				}
			}
			next := r.turnContextForDevices(7, "grp_1", "group", 42, "cloud", devices, nil)
			if len(next.ExplicitGrants) != 0 || len(next.Grants) != 1 {
				t.Fatal("removed canary did not return to default-only")
			}
			delegated := r.turnContextForOwnerDevices(8, 7, "grp_1", "group", 43, "cloud", devices, nil)
			if len(delegated.ExplicitGrants) != 0 {
				t.Fatal("canary expanded delegated access")
			}
		})
	}
}
