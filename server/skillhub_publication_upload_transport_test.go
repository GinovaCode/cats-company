package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationUploadTestStore struct {
	*publicationCaptureTestStore
	record  *store.SkillHubPublicationCaptureRecord
	readErr error
	issues  int
	issued  store.SkillHubPublicationUploadInput
	onIssue func()
}

func (s *publicationUploadTestStore) ReadSkillHubPublicationCapture(context.Context, string, store.SkillHubPublicationDeliveryBinding) (*store.SkillHubPublicationCaptureRecord, error) {
	return s.record, s.readErr
}
func (s *publicationUploadTestStore) IssueSkillHubPublicationUploadGrant(_ context.Context, in store.SkillHubPublicationUploadInput) (*store.SkillHubPublicationUploadGrant, store.SkillHubPublicationUploadToken, error) {
	s.issues++
	s.issued = in
	if s.onIssue != nil {
		s.onIssue()
	}
	token, _ := store.ParseSkillHubPublicationUploadToken("pubupload_" + strings.Repeat("e", 64))
	return &store.SkillHubPublicationUploadGrant{Input: in, Epoch: 1, IdempotencyKey: "pubcandidate_" + strings.Repeat("f", 32), ExpiresAt: time.Now().Add(time.Minute)}, token, nil
}
func (*publicationUploadTestStore) CheckSkillHubPublicationUploadGrant(context.Context, store.SkillHubPublicationUploadToken, store.SkillHubPublicationUploadInput, int) (*store.SkillHubPublicationUploadGrant, error) {
	panic("runtime ACK must not introspect or accept Cloud receipt")
}

func newUploadTransportFixture(t *testing.T) (*publicationWorkspaceFixture, *publicationUploadTestStore) {
	t.Helper()
	var s *publicationUploadTestStore
	f, _ := newCaptureTransportFixtureWithStore(t, func(base *publicationCaptureTestStore) store.Store {
		s = &publicationUploadTestStore{publicationCaptureTestStore: base}
		return s
	})
	d, err := f.h.userDevices.register(7, RegisterUserDeviceRequest{BotUID: 42, DeviceID: f.device.DeviceID, BodyID: f.device.BodyID, InstallationID: f.device.InstallationID, RuntimeRole: "server", Status: "online",
		Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe), string(DeviceCapabilitySkillHubPublicationCapture), string(DeviceCapabilitySkillHubPublicationUpload)}})
	if err != nil {
		t.Fatal(err)
	}
	f.h.bindDeviceClient(7, d, f.runtime)
	b := store.SkillHubPublicationDeliveryBinding{ActorUID: 7, BotUID: 42, Source: s.entries["capture-click"].Input.Source, NodeID: f.h.nodeID, ConnectionID: f.runtime.connectionID}
	s.record = &store.SkillHubPublicationCaptureRecord{Input: store.SkillHubPublicationCaptureInput{EntryID: publicationReadTestID, Binding: b, Revision: 1, RequestHash: strings.Repeat("c", 64), SourceSnapshotHash: strings.Repeat("d", 64), RequestBytes: 1234}, CapturedAt: time.Now()}
	f.h.publicationUpload = newPublicationUploadTransport(f.h, "https://cats.example", "https://hub.example")
	if f.h.publicationUpload == nil {
		t.Fatal("missing transport")
	}
	return f, s
}
func startUploadCall(t *testing.T, f *publicationWorkspaceFixture, ctx context.Context) (*MsgThinToolRPC, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f.h.UploadSkillHubPublication(ctx, 7, publicationReadTestID) }()
	var m ServerMessage
	select {
	case b := <-f.runtime.send:
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatal("early upload exit", err)
	case <-time.After(2 * time.Second):
		t.Fatal("upload dispatch timeout")
	}
	if m.ThinToolRPC == nil || !strings.HasPrefix(m.ThinToolRPC.RequestID, publicationUploadRequestPrefix) {
		t.Fatal("wrong request")
	}
	return m.ThinToolRPC, done
}
func uploadReply(req *MsgThinToolRPC) *MsgThinToolRPC {
	r := captureReply(req)
	r.Result = map[string]interface{}{"schemaVersion": float64(1), "entryId": publicationReadTestID, "authorityEpoch": float64(1), "accepted": true}
	return r
}
func uploadWait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not settle")
		return nil
	}
}

func TestSkillHubPublicationUploadTransportBoundGrantAndScalarACK(t *testing.T) {
	f, s := newUploadTransportFixture(t)
	f.h.removeClient(f.owner)
	req, done := startUploadCall(t, f, context.Background())
	g := req.Payload["grant"].(map[string]interface{})
	if len(req.Payload) != 5 || len(g) != 13 || g["token"] != "pubupload_"+strings.Repeat("e", 64) || g["requestHash"] != s.record.Input.RequestHash || g["requestBytes"] != float64(1234) ||
		g["binding"].(map[string]interface{})["ownerUid"] != "7" || req.ToolName != string(DeviceCapabilitySkillHubPublicationUpload) {
		t.Fatal("grant is not byte/owner bound")
	}
	f.h.handleThinToolRPCResult(f.owner, uploadReply(req))
	deliveryAckStatus(t, f.owner, http.StatusForbidden)
	f.h.handleThinToolRPCResult(f.runtime, uploadReply(req))
	if err := uploadWait(t, done); err != nil {
		t.Fatal(err)
	}
	deliveryAckStatus(t, f.runtime, 200)
	f.h.handleThinToolRPCResult(f.runtime, uploadReply(req))
	deliveryAckStatus(t, f.runtime, 404)
	if s.issues != 1 || s.issued.Binding != s.record.Input.Binding || s.calls != 0 || s.entries["capture-click"].State != "waiting_runtime" || len(f.owner.send) != 0 {
		t.Fatal("ACK wrote publication state or leaked to browser")
	}
}
func TestSkillHubPublicationUploadTransportRejectsMalformedACK(t *testing.T) {
	for _, name := range []string{"epoch", "entry", "extra", "accepted", "tool", "owner", "device", "payload", "type", "error", "retry", "switching", "expired"} {
		t.Run(name, func(t *testing.T) {
			f, _ := newUploadTransportFixture(t)
			req, done := startUploadCall(t, f, context.Background())
			r := uploadReply(req)
			m := r.Result.(map[string]interface{})
			expected := store.ErrSkillHubPublicationInvalid
			switch name {
			case "epoch":
				m["authorityEpoch"] = float64(2)
			case "entry":
				m["entryId"] = "other"
			case "extra":
				m["token"] = "never retained"
			case "accepted":
				m["accepted"] = false
			case "tool":
				r.ToolName = "skillhub.localSkill.share"
			case "owner":
				r.TargetOwnerUserID = "usr8"
			case "device":
				r.DeviceID = "other"
			case "payload":
				r.Payload = map[string]interface{}{"secret": "x"}
			case "type":
				r.Type = thinToolRPCTypeRequest
			case "error":
				r.Error = &MsgDeviceRPCError{Code: "private", Message: "do not surface"}
			case "retry":
				r.Result = nil
				r.Error = &MsgDeviceRPCError{Code: "publication.upload_timeout", Message: "do not surface"}
				expected = store.ErrSkillHubPublicationBusy
			case "switching", "expired":
				code := "BOT_NOT_ACTIVE"
				if name == "expired" {
					code = "REQUEST_EXPIRED"
				}
				r.Result = nil
				r.Error = &MsgDeviceRPCError{Code: code, Message: "do not surface"}
				expected = store.ErrSkillHubPublicationBusy
			}
			f.h.handleThinToolRPCResult(f.runtime, r)
			if err := uploadWait(t, done); !errors.Is(err, expected) {
				t.Fatal(err)
			}
			deliveryAckStatus(t, f.runtime, 422)
		})
	}
}
func TestSkillHubPublicationUploadTransportAdmissionAndAuthority(t *testing.T) {
	for _, name := range []string{"cancelled", "no-capture", "db-denied", "wrong-source", "oversize", "owner", "disabled", "busy", "slots", "expired", "legacy", "changed-after-grant"} {
		t.Run(name, func(t *testing.T) {
			f, s := newUploadTransportFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch name {
			case "cancelled":
				s.entries["capture-click"].State = "cancelled"
			case "no-capture":
				s.record = nil
			case "db-denied":
				s.readErr = store.ErrSkillHubPublicationDenied
			case "wrong-source":
				s.record.Input.Binding.Source.LocalSkillID = "other"
			case "oversize":
				s.record.Input.RequestBytes = publicationCaptureRuntimeMaxBytes + 1
			case "owner":
				s.config.OwnerID = 8
			case "disabled":
				f.h.publicationUpload = nil
			case "busy":
				f.h.publicationUpload.active[publicationReadTestID] = true
			case "slots":
				for i := 0; i < 4; i++ {
					f.h.publicationUpload.slots <- struct{}{}
				}
			case "expired":
				cancel()
			case "legacy":
				_, err := f.h.userDevices.register(7, RegisterUserDeviceRequest{BotUID: 42, DeviceID: f.device.DeviceID, BodyID: f.device.BodyID, InstallationID: f.device.InstallationID, RuntimeRole: "server", Status: "online", Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe), string(DeviceCapabilitySkillHubPublicationCapture)}})
				if err != nil {
					t.Fatal(err)
				}
			case "changed-after-grant":
				s.onIssue = func() { s.config.OwnerID = 8 }
			}
			if err := f.h.UploadSkillHubPublication(ctx, 7, publicationReadTestID); err == nil {
				t.Fatal("bypassed authority")
			}
			if len(f.runtime.send) != 0 || (name != "changed-after-grant" && s.issues != 0) {
				t.Fatal("unauthorized grant or dispatch")
			}
		})
	}
}
func TestSkillHubPublicationUploadTransportLateAndChangedRoute(t *testing.T) {
	for _, mode := range []string{"timeout", "generation", "owner"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newUploadTransportFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			req, done := startUploadCall(t, f, ctx)
			if mode == "timeout" {
				if err := uploadWait(t, done); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				f.h.handleThinToolRPCResult(f.runtime, uploadReply(req))
				deliveryAckStatus(t, f.runtime, 404)
			} else {
				if mode == "owner" {
					s.config.OwnerID = 8
				} else {
					s.generation++
				}
				f.h.handleThinToolRPCResult(f.runtime, uploadReply(req))
				if err := uploadWait(t, done); !errors.Is(err, store.ErrSkillHubPublicationStale) {
					t.Fatal(err)
				}
				deliveryAckStatus(t, f.runtime, 409)
			}
			w := f.h.publicationUpload
			if len(w.pending) != 0 || len(w.active) != 0 || len(w.slots) != 0 {
				t.Fatal("leaked capacity")
			}
		})
	}
}
func TestSkillHubPublicationUploadTransportDefaultOffReserved(t *testing.T) {
	f, _ := newUploadTransportFixture(t)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_UPLOAD_ENABLED", "")
	if newPublicationUploadTransportFromEnv(f.h) != nil {
		t.Fatal("default enabled")
	}
	for _, pair := range [][2]string{{"http://cats.example", "https://hub.example"}, {"https://same.example", "https://same.example"}, {"https://cats.example", "https://hub.example/path"}} {
		if newPublicationUploadTransport(f.h, pair[0], pair[1]) != nil {
			t.Fatal("unsafe pin")
		}
	}
	req := publicationWorkspaceRequest()
	req.ToolName = string(DeviceCapabilitySkillHubPublicationUpload)
	f.h.handleThinToolRPCRequest(f.owner, req)
	if len(f.runtime.send) != 0 {
		t.Fatal("browser invoked upload")
	}
	f.h.publicationUpload = nil
	f.h.handleThinToolRPCResult(f.runtime, &MsgThinToolRPC{ID: "disabled", RequestID: publicationUploadRequestPrefix + "missing", Type: thinToolRPCTypeResult})
	deliveryAckStatus(t, f.runtime, 503)
}
