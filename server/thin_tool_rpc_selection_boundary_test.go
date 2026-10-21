package server

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// Exercise actual message dispatch with in-memory device queues, not real tools.
// Changing selection blocks old grants but does not cancel already-dispatched
// work or relabel its result as coming from the new computer.
func TestThinToolRPCSelectionDispatchBoundary(t *testing.T) {
	hub := NewHub(&agentTestStore{owners: map[int64]int64{42: 8}}, nil)
	bot := &Client{uid: 42, accountType: types.AccountBot, bodyID: "cloud-body", send: make(chan []byte, 16)}
	hub.addClient(bot)
	devices := make(map[string]UserDevice)
	receivers := make(map[string]*Client)
	for _, id := range []string{"a", "b"} {
		device, err := hub.userDevices.register(7, RegisterUserDeviceRequest{
			DeviceID: id, BodyID: "body-" + id, InstallationID: "install-" + id,
			RuntimeRole: "desktop", Status: "online", Capabilities: []string{"read_file"},
		})
		if err != nil {
			t.Fatal(err)
		}
		receiver := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 16)}
		hub.addClient(receiver)
		hub.bindDeviceClient(7, device, receiver)
		devices[id], receivers[id] = device, receiver
	}
	session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
	first, err := hub.userDevices.confirmDeviceSelection(7, session, "a", "user_confirmation", "")
	if err != nil {
		t.Fatal(err)
	}
	grantA := hub.userDevices.grantsForDevices(7, "p2p_7_42", "p2p", 42, "cloud-body", []UserDevice{devices["a"]})[0]
	request := func(id string, grant ScopedDeviceGrant) *MsgThinToolRPC {
		return &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: id, GrantID: grant.GrantID,
			ActorUserID: grant.ActorUserID, SessionKey: grant.SessionKey,
			TargetOwnerUserID: "usr7", TargetDeviceID: grant.DeviceID, ToolName: "read_file"}
	}
	read := func(client *Client) *MsgThinToolRPC {
		t.Helper()
		for {
			var msg ServerMessage
			decodeQueuedServerMessage(t, client.send, &msg)
			if msg.ThinToolRPC != nil {
				return msg.ThinToolRPC
			}
			if msg.Ctrl == nil {
				t.Fatalf("expected RPC or control acknowledgement, got %#v", msg)
			}
		}
	}
	noRPC := func(client *Client) {
		t.Helper()
		for len(client.send) > 0 {
			var msg ServerMessage
			decodeQueuedServerMessage(t, client.send, &msg)
			if msg.ThinToolRPC != nil {
				t.Fatalf("unexpected RPC: %#v", msg.ThinToolRPC)
			}
		}
	}
	hub.handleMessage(bot, &ClientMessage{ThinToolRPC: request("already-sent-a", grantA)})
	if got := read(receivers["a"]); got.TargetDeviceID != "a" || got.Error != nil {
		t.Fatalf("initial A dispatch: %#v", got)
	}
	if _, err := hub.userDevices.confirmDeviceSelection(7, session, "b", "user_confirmation", first.Revision); err != nil {
		t.Fatal(err)
	}
	if _, ok := hub.thinToolRPC.get("already-sent-a"); !ok {
		t.Fatal("switch silently discarded already-dispatched A work")
	}
	hub.handleMessage(bot, &ClientMessage{ThinToolRPC: request("stale-a", grantA)})
	if got := read(bot); got.Error == nil || got.RequestID != "stale-a" {
		t.Fatalf("old grant was not explicitly rejected: %#v", got)
	}
	if drainOne(receivers["a"].send) || drainOne(receivers["b"].send) {
		t.Fatal("stale request executed or was retargeted to B")
	}
	grantB := hub.userDevices.grantsForDevices(7, "p2p_7_42", "p2p", 42, "cloud-body", []UserDevice{devices["b"]})[0]
	hub.handleMessage(bot, &ClientMessage{ThinToolRPC: request("fresh-b", grantB)})
	if got := read(receivers["b"]); got.TargetDeviceID != "b" || got.Error != nil {
		t.Fatalf("new B dispatch: %#v", got)
	}
	// B cannot claim A's in-flight result even though B is now selected.
	hub.handleMessage(receivers["b"], &ClientMessage{ThinToolRPC: &MsgThinToolRPC{
		Type: thinToolRPCTypeResult, RequestID: "already-sent-a", DeviceID: "a", Result: map[string]interface{}{"content": "fake"},
	}})
	noRPC(bot)
	hub.handleMessage(receivers["a"], &ClientMessage{ThinToolRPC: &MsgThinToolRPC{
		Type: thinToolRPCTypeResult, RequestID: "already-sent-a", DeviceID: "a", Result: map[string]interface{}{"content": "from A"},
	}})
	if got := read(bot); got.RequestID != "already-sent-a" || got.TargetDeviceID != "a" || got.DeviceID != "a" || got.Error != nil {
		t.Fatalf("A's completion was lost or mislabeled after switching: %#v", got)
	}
	if _, ok := hub.thinToolRPC.get("already-sent-a"); ok {
		t.Fatal("completed A pending retained")
	}
	if _, ok := hub.thinToolRPC.get("fresh-b"); !ok {
		t.Fatal("A's completion consumed B's request")
	}
}
