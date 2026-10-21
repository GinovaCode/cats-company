package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

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
		{"invalid uid", "1", "0,-1", 0, false},
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
