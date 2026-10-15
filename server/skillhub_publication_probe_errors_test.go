package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSkillHubPublicationWorkspaceAndDeliveryRequestErrors(t *testing.T) {
	for _, tc := range []struct {
		code, workspaceCode, deliveryCode, state string
	}{
		{"INVALID_REQUEST", "publication.runtime_unsupported", "publication.runtime_unsupported", "needs_user_action"},
		{"OWNER_MISMATCH", "publication.runtime_unsupported", "publication.runtime_unsupported", "needs_user_action"},
		{"DEVICE_MISMATCH", "publication.runtime_unsupported", "publication.runtime_unsupported", "needs_user_action"},
		{"BOT_NOT_ACTIVE", "publication.runtime_unsupported", "publication.runtime_unsupported", "needs_user_action"},
		{"REQUEST_EXPIRED", "publication.runtime_timeout", "publication.runtime_timeout", "pending"},
		{"publication.source_changed", "publication.source_changed", "publication.source_changed", "needs_user_action"},
		{"publication.runtime_unavailable", "publication.source_unavailable", "publication.runtime_unavailable", "pending"},
		{"unknown-secret", "publication.source_unavailable", "publication.runtime_unavailable", "pending"},
		{"", "publication.source_unavailable", "publication.runtime_unavailable", "pending"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Run("workspace", func(t *testing.T) {
				f := newPublicationWorkspaceFixture(t)
				probe := f.start(t, publicationWorkspaceRequest())
				reply := f.result(probe)
				reply.Result = nil
				reply.Error = &MsgDeviceRPCError{Code: tc.code, Message: "/root/secret Bearer secret"}
				f.h.handleThinToolRPCResult(f.runtime, reply)
				result := publicationWorkspaceResponse(t, f.owner)
				if result.Error == nil || result.Error.Code != tc.workspaceCode || f.db.creates != 0 {
					t.Fatalf("result=%+v creates=%d", result, f.db.creates)
				}
				encoded, _ := json.Marshal(result)
				if strings.Contains(string(encoded), "secret") {
					t.Fatal("raw runtime error exposed")
				}
			})
			t.Run("delivery", func(t *testing.T) {
				f, s, w := newPublicationDeliveryFixture(t)
				w.sweep()
				probe := deliveryProbe(t, f.runtime)
				reply := f.result(probe)
				reply.Result = nil
				reply.Error = &MsgDeviceRPCError{Code: tc.code, Message: "/root/secret Bearer secret"}
				f.h.handleThinToolRPCResult(f.runtime, reply)
				deliveryProcess(t, w)
				deliveryAckStatus(t, f.runtime, 200)
				job := s.jobs[publicationReadTestID]
				if job.state != tc.state || job.code != tc.deliveryCode {
					t.Fatalf("job=%+v", job)
				}
				// Terminal request failures must not be dispatched again.
				w.sweep()
				if tc.state == "needs_user_action" && len(f.runtime.send) != 0 {
					t.Fatal("invalid request retried")
				}
			})
		})
	}
}
