package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func TestSkillHubPublicationUploadDispatcherDefaultOffAndRuntimeAck(t *testing.T) {
	f, s := newUploadTransportFixture(t)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_UPLOAD_DISPATCH_ENABLED", "")
	if newPublicationUploadDispatcherFromEnv(f.h) != nil {
		t.Fatal("upload dispatcher enabled by default")
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_UPLOAD_DISPATCH_ENABLED", "true")
	w := newPublicationUploadDispatcherFromEnv(f.h)
	if w == nil {
		t.Fatal("upload dispatcher did not initialize")
	}
	done := make(chan error, 1)
	go func() { done <- w.runOnce(context.Background()) }()
	var msg ServerMessage
	select {
	case raw := <-f.runtime.send:
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upload request not dispatched")
	}
	if msg.ThinToolRPC == nil || msg.ThinToolRPC.ToolName != string(DeviceCapabilitySkillHubPublicationUpload) {
		t.Fatal("wrong upload request")
	}
	f.h.handleThinToolRPCResult(f.runtime, uploadReply(msg.ThinToolRPC))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upload dispatcher did not settle")
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if s.deliveryOutcome != "uploaded" {
		t.Fatalf("runtime ACK outcome = %q", s.deliveryOutcome)
	}
}

func TestSkillHubPublicationUploadOutcomeClassification(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{nil, "uploaded"},
		{store.ErrSkillHubPublicationInvalid, "rejected"},
		{store.ErrSkillHubPublicationDenied, "rejected"},
		{store.ErrSkillHubPublicationConflict, "rejected"},
		{store.ErrSkillHubPublicationSettled, "rejected"},
		{context.DeadlineExceeded, "retry"},
	} {
		if got := publicationUploadOutcome(test.err); got != test.want {
			t.Fatalf("outcome(%v)=%q, want %q", test.err, got, test.want)
		}
	}
}
