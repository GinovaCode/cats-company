package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCloudWorkerAdminProvisionRejectsAccountWithExistingWorker pins the
// duplicate guard: the operator console offers "provision" only when the worker
// count reads as zero, but a stale or failed count must not be able to create a
// second billable instance.
func TestCloudWorkerAdminProvisionRejectsAccountWithExistingWorker(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("")
	ts.ownerBots = []map[string]interface{}{
		{"id": int64(1611), "username": "bot-1601-abcdef", "tenant_name": "bot-1601-abcdef"},
	}
	body := `{"uid":1601,"source_ref":"provision-guard-1","deployment_profile":"private_nat","billing_mode":"month"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloud-workers/provision", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleAdminProvision(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want %d (body=%s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cloud_worker_exists") {
		t.Fatalf("expected cloud_worker_exists code, got %s", rec.Body.String())
	}
}

// TestCloudWorkerAdminProvisionPassesAccountsWithoutWorker makes sure the guard
// does not reject an account that owns nothing: the request must get past it and
// stop on a later check instead.
func TestCloudWorkerAdminProvisionPassesAccountsWithoutWorker(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("")
	ts.ownerBots = nil
	body := `{"uid":1601,"source_ref":"provision-guard-2","deployment_profile":"private_nat","billing_mode":"month"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloud-workers/provision", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleAdminProvision(rec, req)

	if rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), "cloud_worker_exists") {
		t.Fatalf("an account without a worker must not hit the duplicate guard: %s", rec.Body.String())
	}
}

// TestCloudWorkerAdminProvisionFailsClosedWhenWorkerLookupFails keeps the guard
// fail-closed: if the ownership check itself errors, the action must not create
// a paid instance that a retry cannot undo.
func TestCloudWorkerAdminProvisionFailsClosedWhenWorkerLookupFails(t *testing.T) {
	h, ts := newCloudWorkerTestHandler("")
	ts.listBotsErr = errWorkerLookupUnavailable
	body := `{"uid":1601,"source_ref":"provision-guard-3","deployment_profile":"private_nat","billing_mode":"month"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cloud-workers/provision", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleAdminProvision(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d (body=%s)", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

type workerLookupError struct{}

func (workerLookupError) Error() string { return "bot store unavailable" }

var errWorkerLookupUnavailable = workerLookupError{}
