package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSkillHubPublicationWorkspaceAndDeliveryRequestErrors(t *testing.T) {
	for _, tc := range []struct {
		code, workspaceCode, deliveryCode, state string
	}{
		{"INVALID_REQUEST", "publication.runtime_unsupported", "publication.runtime_unsupported", "needs_user_action"},
		{"OWNER_MISMATCH", "publication.runtime_unsupported", "publication.runtime_unsupported", "needs_user_action"},
		{"DEVICE_MISMATCH", "publication.runtime_unsupported", "publication.runtime_unsupported", "needs_user_action"},
		{"BOT_NOT_ACTIVE", "publication.source_unavailable", "publication.runtime_unavailable", "pending"},
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
				if tc.state == "needs_user_action" {
					job.next = time.Now().Add(-time.Second)
				}
				w.sweep()
				if tc.state == "needs_user_action" && len(f.runtime.send) != 0 {
					t.Fatal("invalid request retried")
				}
			})
		})
	}
}

func TestSkillHubPublicationDeliveryBotNotActiveRecovery(t *testing.T) {
	for _, transferred := range []bool{false, true} {
		name := "workspace recovers"
		if transferred {
			name = "authority changes before retry"
		}
		t.Run(name, func(t *testing.T) {
			f, s, w := newPublicationDeliveryFixture(t)
			w.sweep()
			first := deliveryProbe(t, f.runtime)
			oldLease := s.jobs[publicationReadTestID].lease
			reply := f.result(first)
			reply.Result = nil
			reply.Error = &MsgDeviceRPCError{Code: "BOT_NOT_ACTIVE", Message: "private workspace switching"}
			f.h.handleThinToolRPCResult(f.runtime, reply)
			deliveryProcess(t, w)
			deliveryAckStatus(t, f.runtime, 200)
			job := s.jobs[publicationReadTestID]
			if job.state != "pending" || job.code != "publication.runtime_unavailable" {
				t.Fatalf("transient failure was not retryable: %+v", job)
			}
			w.sweep()
			if len(f.runtime.send) != 0 || s.claims != 1 {
				t.Fatal("retry ignored backoff")
			}
			job.next = time.Now().Add(-time.Second)
			if transferred {
				s.config.OwnerID = 9
			}
			w.sweep()
			if transferred {
				if len(f.runtime.send) != 0 || s.claims != 1 {
					t.Fatal("retry bypassed current authority")
				}
				return
			}
			second := deliveryProbe(t, f.runtime)
			newLease := s.jobs[publicationReadTestID].lease
			if second.RequestID == first.RequestID || newLease.Token == oldLease.Token || newLease.Attempt != oldLease.Attempt+1 || newLease.Binding != oldLease.Binding || s.creates != 1 {
				t.Fatal("retry did not preserve intent with a fresh delivery lease")
			}
			// The previous delivery cannot settle the new attempt.
			f.h.handleThinToolRPCResult(f.runtime, f.result(first))
			deliveryAckStatus(t, f.runtime, 404)
			if s.jobs[publicationReadTestID].state != "leased" {
				t.Fatal("old receipt settled retry")
			}
			f.h.handleThinToolRPCResult(f.runtime, f.result(second))
			deliveryProcess(t, w)
			deliveryAckStatus(t, f.runtime, 200)
			job = s.jobs[publicationReadTestID]
			if job.state != "source_verified" || job.code != "" || s.creates != 1 {
				t.Fatalf("recovered workspace did not settle original intent: %+v", job)
			}
		})
	}
}
