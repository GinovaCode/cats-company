package server

import (
	"testing"
	"time"
)

func deviceGrantForID(t *testing.T, grants []ScopedDeviceGrant, id string) ScopedDeviceGrant {
	t.Helper()
	for _, grant := range grants {
		if grant.DeviceID == id {
			return grant
		}
	}
	t.Fatalf("missing grant for %s: %#v", id, grants)
	return ScopedDeviceGrant{}
}

func TestExplicitDeviceTargetsKeepConversationDefault(t *testing.T) {
	for _, backend := range []string{"memory", "shared", "durable"} {
		t.Run(backend, func(t *testing.T) {
			r := newUserDeviceRegistry(time.Hour)
			if backend == "shared" {
				r.withSharedRuntime(newSharedMemoryRuntimeState())
			}
			if backend == "durable" {
				r.withSelectionStore(&durableSelectionTestStore{})
			}
			a := UserDevice{OwnerUID: 7, DeviceID: "a", DisplayName: "Eason", OS: "windows", Capabilities: []DeviceGrantOperation{DeviceGrantWriteFile}}
			b := UserDevice{OwnerUID: 7, DeviceID: "b", DisplayName: "FFFFFFFK", OS: "windows", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
			foreign := UserDevice{OwnerUID: 8, DeviceID: "private", Capabilities: []DeviceGrantOperation{DeviceGrantWriteFile}}
			session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
			first, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", "")
			if err != nil {
				t.Fatal(err)
			}
			var oldGrants []ScopedDeviceGrant
			for _, text := range []string{"在桌面创建文件", "在 FFFFFFFK 上读取文件", "在两台设备都创建文件", "不要操作 FFFFFFFK"} {
				ctx := r.turnContextForOwnerDevices(7, 7, "p2p_7_42", "p2p", 42, "cloud", text, []UserDevice{b, foreign, a}, nil)
				if ctx.Selection.Status != DeviceSelectionSelected || ctx.Selection.SelectedDevice.DeviceID != "a" || ctx.Selection.Revision != first.Revision {
					t.Fatalf("text changed default: %#v", ctx.Selection)
				}
				if len(ctx.Grants) != 2 || len(ctx.Selection.Candidates) != 2 {
					t.Fatal("missing own device or leaked another account")
				}
				for _, grant := range ctx.Grants {
					if grant.TargetScope != "explicit_device" || grant.SelectionRevision != first.Revision || grant.OwnerUserID != "usr7" {
						t.Fatalf("invalid grant: %#v", grant)
					}
					if _, ok := r.lookupGrant(grant.GrantID); !ok {
						t.Fatal("own explicit grant rejected")
					}
				}
				bGrant := deviceGrantForID(t, ctx.Grants, "b")
				if len(bGrant.Operations) != 1 || bGrant.Operations[0] != DeviceGrantReadFile {
					t.Fatal("per-device capabilities widened")
				}
				oldGrants = append(oldGrants, ctx.Grants...)
			}
			// Both task-specific grants are invalid after a UI selection change,
			// including a grant for the newly selected device.
			if _, err := r.confirmDeviceSelection(7, session, "b", "user_confirmation", first.Revision); err != nil {
				t.Fatal(err)
			}
			for _, grant := range oldGrants {
				if _, ok := r.lookupGrant(grant.GrantID); ok {
					t.Fatal("pre-switch grant survived")
				}
			}
		})
	}
}

func TestExplicitTargetsWithoutDefaultAndWithOfflineDefault(t *testing.T) {
	r := newUserDeviceRegistry(time.Hour)
	a := UserDevice{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
	b := UserDevice{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}}
	ctx := r.turnContextForOwnerDevices(7, 7, "grp_1", "group", 42, "cloud", "两台都读取", []UserDevice{a, b}, nil)
	if ctx.Selection.Status != DeviceSelectionNeedsSelection || ctx.Selection.SelectedDevice != nil || len(ctx.Grants) != 2 {
		t.Fatalf("explicit task should not auto-select a default: %#v", ctx)
	}
	for _, grant := range ctx.Grants {
		if _, ok := r.lookupGrant(grant.GrantID); !ok {
			t.Fatal("grant without default rejected")
		}
	}
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	if _, err := r.confirmDeviceSelection(7, session, "a", "user_confirmation", ""); err != nil {
		t.Fatal(err)
	}
	ctx = r.turnContextForOwnerDevices(7, 7, "grp_1", "group", 42, "cloud", "在 b 上读取", []UserDevice{b}, []UserDevice{a})
	if ctx.Selection.Status != DeviceSelectionUnavailable || ctx.Selection.SelectedDevice.DeviceID != "a" || len(ctx.Grants) != 1 || ctx.Grants[0].DeviceID != "b" {
		t.Fatalf("offline default was replaced or explicit B denied: %#v", ctx)
	}
	if _, ok := r.lookupGrant(ctx.Grants[0].GrantID); !ok {
		t.Fatal("explicit B denied because default A is offline")
	}
	old := ctx.Grants[0]
	now := time.Now().Add(r.preferenceTTL + time.Minute)
	r.now = func() time.Time { return now }
	ctx = r.turnContextForOwnerDevices(7, 7, "grp_1", "group", 42, "cloud", "在 b 上读取", []UserDevice{b}, []UserDevice{a})
	if ctx.Selection.Status != DeviceSelectionNeedsSelection || ctx.Selection.SelectedDevice != nil || len(ctx.Grants) != 1 {
		t.Fatal("expired default must not choose B or block an independent B grant")
	}
	if _, ok := r.lookupGrant(old.GrantID); ok {
		t.Fatal("expired old grant revived")
	}
	if _, ok := r.lookupGrant(ctx.Grants[0].GrantID); !ok {
		t.Fatal("fresh exact-target grant rejected")
	}
}

func TestDelegatedChannelDevicesRemainSelectedOnly(t *testing.T) {
	r := newUserDeviceRegistry(time.Hour)
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 8)
	if _, err := r.confirmDeviceSelection(8, session, "a", "test", ""); err != nil {
		t.Fatal(err)
	}
	devices := []UserDevice{
		{OwnerUID: 7, DeviceID: "a", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
		{OwnerUID: 7, DeviceID: "b", Capabilities: []DeviceGrantOperation{DeviceGrantReadFile}},
	}
	ctx := r.turnContextForOwnerDevices(8, 7, "grp_1", "group", 42, "cloud", "两台都读取", devices, nil)
	if len(ctx.Grants) != 1 || ctx.Grants[0].DeviceID != "a" || ctx.Grants[0].TargetScope != "" {
		t.Fatal("same-account policy leaked into delegated channel flow")
	}
}

func TestExplicitTargetAfterPickerSnapshotWithoutDefault(t *testing.T) {
	hub, handler := deviceSelectionFixture(t)
	snapshot := selectionSnapshot(t, handler, 7, "p2p_7_42", "42")
	if snapshot["status"] != "needs_selection" {
		t.Fatal("fixture unexpectedly has a default")
	}
	devices, unavailable := hub.userDeviceRouteCandidates(7)
	ctx := hub.userDevices.turnContextForDevices(7, "p2p_7_42", "p2p", 42, "cloud", "在 b 上读取", devices, unavailable)
	if ctx.Selection.SelectedDevice != nil || ctx.Selection.Revision != snapshot["revision"] || len(ctx.Grants) != 2 {
		t.Fatal("text chose a default or lost snapshot revision")
	}
	for _, grant := range ctx.Grants {
		if _, ok := hub.userDevices.lookupGrant(grant.GrantID); !ok {
			t.Fatal("empty picker slot rejected exact target grant")
		}
	}
}
