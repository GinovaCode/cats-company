package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// These are isolated regression specifications: no production account,
// socket or filesystem tool is used. They intentionally fail on the baseline.
func TestMultiDeviceRegressionDirectoryContainsBothComputers(t *testing.T) {
	hub := NewHub(&identityMessageStore{users: map[int64]*types.User{
		7:  {ID: 7, Username: "audit-alice", AccountType: types.AccountHuman},
		42: {ID: 42, Username: "audit-bot", AccountType: types.AccountBot},
	}}, nil)
	now := time.Now()
	hub.userDevices.now = func() time.Time { return now }
	for _, id := range []string{"audit-device-a", "audit-device-b"} {
		device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
			DeviceID: id, DisplayName: id, RuntimeRole: "desktop", Status: "online",
			Capabilities: []string{"read_file"},
		})
		if err != nil {
			t.Fatal(err)
		}
		client := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
		hub.addClient(client)
		hub.bindDeviceClient(7, device, client)
		now = now.Add(time.Millisecond)
	}
	routable, _ := hub.userDeviceRouteCandidates(7)
	if len(routable) != 2 {
		t.Fatalf("invalid fixture: got %d live device routes, want 2", len(routable))
	}
	metadata := hub.buildXiaobaRuntimeMetadata(7, 42, "p2p_7_42")
	devices, _ := metadata["devices"].([]map[string]interface{})
	if len(devices) != 2 {
		t.Fatalf("account has two live computers; model directory contains %d, want 2", len(devices))
	}
	// A stops heartbeating while B remains active. A must remain in the
	// directory as unavailable, rather than disappearing behind B's alias.
	now = now.Add(hub.userDevices.ttl + time.Second)
	hub.userDevices.touch(7, "audit-device-b")
	metadata = hub.buildXiaobaRuntimeMetadata(7, 42, "p2p_7_42")
	devices = metadata["devices"].([]map[string]interface{})
	if len(devices) != 2 || devices[0]["deviceId"] != "audit-device-a" || devices[0]["status"] != "unavailable" || devices[1]["status"] != "ready" {
		t.Fatalf("offline A must remain visible without changing stable ordering: %#v", devices)
	}
}

func TestMultiDeviceExpiredPreferenceMustNotChooseOnlyOnlineDevice(t *testing.T) {
	r := newUserDeviceRegistry(time.Hour)
	now := time.Now()
	r.now = func() time.Time { return now }
	a := UserDevice{OwnerUID: 7, DeviceID: "audit-a", DisplayName: "Laptop A"}
	b := UserDevice{OwnerUID: 7, DeviceID: "audit-b", DisplayName: "Laptop B"}
	session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
	r.rememberDeviceSelection(7, session, a.DeviceID, "test_fixture")
	now = now.Add(r.preferenceTTL + time.Second)
	ctx := r.turnContextForOwnerDevices(7, 7, "p2p_7_42", "p2p", 42, "cloud", "继续读取刚才的文件", []UserDevice{b}, []UserDevice{a})
	if ctx.Selection.Status != DeviceSelectionNeedsSelection || len(ctx.Grants) != 0 {
		t.Fatalf("expired selection must ask instead of choosing online B: %#v", ctx)
	}
}

func TestMultiDevicePreferenceIsolationAndHeartbeatOrdering(t *testing.T) {
	r := newUserDeviceRegistry(time.Hour)
	a := UserDevice{OwnerUID: 7, DeviceID: "audit-a", DisplayName: "Laptop A"}
	b := UserDevice{OwnerUID: 7, DeviceID: "audit-b", DisplayName: "Laptop B"}
	session := buildCatsCoSessionKey("grp_1", "group", "usr42", 7)
	r.rememberDeviceSelection(7, session, a.DeviceID, "test_fixture")
	_, selected := r.selectDeviceForTurn(7, "grp_1", "group", 42, []UserDevice{b, a}, "继续读取")
	if selected == nil || selected.DeviceID != a.DeviceID {
		t.Fatal("heartbeat ordering changed selected A")
	}
	for _, tc := range []struct {
		actor, bot int64
		topic      string
	}{{8, 42, "grp_1"}, {7, 43, "grp_1"}, {7, 42, "grp_2"}} {
		selection, selected := r.selectDeviceForTurn(tc.actor, tc.topic, "group", tc.bot, []UserDevice{a, b}, "继续读取")
		if selected != nil || selection.Status != DeviceSelectionNeedsSelection {
			t.Fatalf("selection leaked across actor/bot/topic: %#v", selection)
		}
	}
}

func TestMultiDeviceRegressionOfflineSelectionMustNotSwitch(t *testing.T) {
	r := newUserDeviceRegistry(time.Hour)
	devices := []UserDevice{
		{OwnerUID: 7, DeviceID: "audit-a", DisplayName: "Audit Laptop A"},
		{OwnerUID: 7, DeviceID: "audit-b", DisplayName: "Audit Laptop B"},
	}
	r.rememberDeviceSelection(7, buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7), "audit-a", "confirmed_fixture")
	_, selected := r.selectDeviceForTurn(7, "p2p_7_42", "p2p", 42, devices, "请在 Audit Laptop A 上读取文件")
	if selected == nil || selected.DeviceID != "audit-a" {
		t.Fatal("fixture did not select A")
	}
	selection, target := r.selectDeviceForTurn(7, "p2p_7_42", "p2p", 42, devices[1:], "删掉刚才的测试文件")
	if target != nil {
		t.Fatalf("offline A silently changed to %s; want unavailable/confirmation, selection=%#v", target.DeviceID, selection)
	}
}

func TestMultiDeviceRegressionMentionIsNotSwitchConsent(t *testing.T) {
	r := newUserDeviceRegistry(time.Hour)
	devices := []UserDevice{
		{OwnerUID: 7, DeviceID: "audit-a", DisplayName: "Audit Laptop A"},
		{OwnerUID: 7, DeviceID: "audit-b", DisplayName: "Audit Laptop B"},
	}
	r.rememberDeviceSelection(7, buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7), "audit-a", "confirmed_fixture")
	_, target := r.selectDeviceForTurn(7, "p2p_7_42", "p2p", 42, devices, "为什么上次切到了 Audit Laptop B？不要切换设备。")
	if target != nil && target.DeviceID != "audit-a" {
		t.Fatalf("mention without switch consent selected %s", target.DeviceID)
	}
}

func TestMultiDeviceRegressionAmbiguousFirstTurnMustAsk(t *testing.T) {
	r := newUserDeviceRegistry(time.Hour)
	selection, target := r.selectDeviceForTurn(7, "p2p_7_42", "p2p", 42, []UserDevice{
		{OwnerUID: 7, DeviceID: "audit-a", DisplayName: "Audit Laptop A"},
		{OwnerUID: 7, DeviceID: "audit-b", DisplayName: "Audit Laptop B"},
	}, "读取我的桌面")
	if target != nil || selection.Status != DeviceSelectionNeedsSelection {
		t.Fatalf("ambiguous first turn must ask; got %#v", selection)
	}
}

func TestMultiDeviceRegressionUnscopedBotRequestMustNotReachOtherAccount(t *testing.T) {
	// The authenticated requester belongs to account 8, while the mock device
	// belongs to account 7. There is no user turn, grant, friendship or session.
	hub := NewHub(&agentTestStore{owners: map[int64]int64{42: 8}}, nil)
	device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
		DeviceID: "audit-other-account-device", RuntimeRole: "desktop", Status: "online",
		Capabilities: []string{"read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requester := &Client{uid: 42, accountType: types.AccountBot, send: make(chan []byte, 8)}
	receiver := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	hub.addClient(requester)
	hub.addClient(receiver)
	hub.bindDeviceClient(7, device, receiver)
	hub.handleMessage(requester, &ClientMessage{ThinToolRPC: &MsgThinToolRPC{
		ID: "audit-request", Type: thinToolRPCTypeRequest, RequestID: "audit-no-session",
		TargetOwnerUserID: "usr7", TargetDeviceID: device.DeviceID, ToolName: "read_file",
		Payload: map[string]interface{}{"file_path": "audit-placeholder-not-a-real-file"},
	}})
	if drainOne(receiver.send) {
		t.Fatal("unscoped Bot request was forwarded to another account's mock device")
	}
	if _, ok := hub.thinToolRPC.get("audit-no-session"); ok {
		t.Fatal("unauthorized request was left pending")
	}
}
