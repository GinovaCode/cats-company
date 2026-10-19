package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type commercialBaselineRepairTestStore struct {
	*commercialTestStore
	candidates []*types.CommercialRelayBaselineCandidate
	revoked    []int64
	ensured    []int64
	ensuredAt  map[int64]time.Time
}

func (s *commercialBaselineRepairTestStore) ListCommercialRelayLegacyBaselines() ([]*types.CommercialRelayBaselineCandidate, error) {
	return s.candidates, nil
}

func (s *commercialBaselineRepairTestStore) RevokeCommercialRelayLegacyBaseline(uid int64, _ string) (bool, error) {
	s.revoked = append(s.revoked, uid)
	return true, nil
}

func (s *commercialBaselineRepairTestStore) EnsureCommercialRelayBaseline(uid int64, _ string, _ map[string]float64, startsAt time.Time) (bool, error) {
	s.ensured = append(s.ensured, uid)
	if s.ensuredAt == nil {
		s.ensuredAt = map[int64]time.Time{}
	}
	s.ensuredAt[uid] = startsAt
	return true, nil
}

func newCommercialBaselineRepairStore() *commercialBaselineRepairTestStore {
	return &commercialBaselineRepairTestStore{
		commercialTestStore: newCommercialTestStore(),
		ensuredAt:           map[int64]time.Time{},
	}
}

func runCommercialBaselineRepair(t *testing.T, store *commercialBaselineRepairTestStore, body string) map[string]interface{} {
	t.Helper()
	handler := &AccountAdminHandler{commercial: store}
	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/baseline-repair", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.HandleCommercialBaselineRepair(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode repair response: %v", err)
	}
	return payload
}

// baselineRepairCandidate builds a signup-time baseline: Relay wrote the
// entitlement moments after the account was created.
func baselineRepairCandidate(uid int64, budgets map[string]float64) *types.CommercialRelayBaselineCandidate {
	created := time.Date(2026, 10, 9, 6, 11, 0, 0, time.UTC)
	return &types.CommercialRelayBaselineCandidate{
		UID:           uid,
		PlanID:        9,
		PlanSlug:      "catsco-legacy-custom",
		SourceRef:     "relay-quota-migration-v1",
		StartsAt:      created.Add(12 * time.Second),
		UserCreatedAt: created,
		Budgets:       budgets,
	}
}

func TestCommercialBaselineRepairDryRunTouchesNothing(t *testing.T) {
	store := newCommercialBaselineRepairStore()
	store.candidates = []*types.CommercialRelayBaselineCandidate{
		baselineRepairCandidate(1601, map[string]float64{
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-flash": 100, "glm-5.3-flash": 100,
		}),
	}
	payload := runCommercialBaselineRepair(t, store, `{"all":true}`)
	if payload["applied"] != false {
		t.Fatalf("dry run must not apply: %#v", payload)
	}
	if len(store.revoked) != 0 || len(store.ensured) != 0 {
		t.Fatalf("dry run mutated state: revoked=%v ensured=%v", store.revoked, store.ensured)
	}
	results, _ := payload["results"].([]interface{})
	if len(results) != 1 {
		t.Fatalf("results=%#v", payload["results"])
	}
	entry, _ := results[0].(map[string]interface{})
	if entry["action"] != "would-repair" {
		t.Fatalf("action=%v want would-repair", entry["action"])
	}
}

func TestCommercialBaselineRepairRebuildsFreeDeterministically(t *testing.T) {
	// The critical property: after revoking, the Free baseline is created
	// directly. Re-classifying the Relay snapshot would fail because an
	// enforced account reports the shared pool total on every model.
	store := newCommercialBaselineRepairStore()
	candidate := baselineRepairCandidate(1009, map[string]float64{
		"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "glm-5.3-flash": 100,
		"gpt-image-2": 100, "gpt-image-2.5": 100, "gpt-image-2.5-flare": 100,
		"gpt-image-2.5-sunburst": 100, "chatgpt-image-latest": 100,
	})
	store.candidates = []*types.CommercialRelayBaselineCandidate{candidate}
	payload := runCommercialBaselineRepair(t, store, `{"all":true,"apply":true}`)
	if payload["repaired"] != float64(1) {
		t.Fatalf("payload=%#v", payload)
	}
	if len(store.revoked) != 1 || store.revoked[0] != 1009 {
		t.Fatalf("revoked=%v", store.revoked)
	}
	if len(store.ensured) != 1 || store.ensured[0] != 1009 {
		t.Fatalf("ensured=%v want the free baseline rebuild", store.ensured)
	}
	if got := store.ensuredAt[1009]; !got.Equal(candidate.StartsAt) {
		t.Fatalf("free baseline starts at %v want %v", got, candidate.StartsAt)
	}
}

func TestCommercialBaselineRepairAppliesOnlyFreePoolSubsets(t *testing.T) {
	store := newCommercialBaselineRepairStore()
	store.candidates = []*types.CommercialRelayBaselineCandidate{
		baselineRepairCandidate(1601, map[string]float64{
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-flash": 100, "glm-5.3-flash": 100,
		}),
		// Real custom pool from the pre-commercial migration.
		baselineRepairCandidate(404, map[string]float64{
			"gpt-5.6-terra": 5000, "MiniMax-M2.7": 1000,
		}),
	}
	payload := runCommercialBaselineRepair(t, store, `{"all":true,"apply":true}`)
	if payload["applied"] != true || payload["repaired"] != float64(1) || payload["skipped"] != float64(1) {
		t.Fatalf("apply response: %#v", payload)
	}
	if len(store.revoked) != 1 || store.revoked[0] != 1601 {
		t.Fatalf("revoked=%v", store.revoked)
	}
}

func TestCommercialBaselineRepairKeepsMigrationBaselines(t *testing.T) {
	// The pre-commercial migration also used relay-quota-migration-v1, but it
	// ran weeks after the accounts were created. Composition alone cannot tell
	// them apart, so the signup window does.
	migration := baselineRepairCandidate(700, map[string]float64{
		"MiniMax-M2.7": 1000, "MiniMax-M3": 500,
	})
	created := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	migration.UserCreatedAt = created
	migration.StartsAt = created.AddDate(0, 3, 0)

	store := newCommercialBaselineRepairStore()
	store.candidates = []*types.CommercialRelayBaselineCandidate{migration}
	payload := runCommercialBaselineRepair(t, store, `{"all":true,"apply":true}`)
	if len(store.revoked) != 0 {
		t.Fatalf("migration baseline was revoked: %v", store.revoked)
	}
	results, _ := payload["results"].([]interface{})
	entry, _ := results[0].(map[string]interface{})
	if entry["reason"] != "baseline was not written at signup time" {
		t.Fatalf("reason=%v", entry["reason"])
	}
}

func TestCommercialBaselineRepairStopsAtTheBatchLimit(t *testing.T) {
	store := newCommercialBaselineRepairStore()
	for uid := int64(1600); uid < 1610; uid++ {
		store.candidates = append(store.candidates,
			baselineRepairCandidate(uid, map[string]float64{"MiniMax-M2.7": 1000}))
	}
	payload := runCommercialBaselineRepair(t, store, `{"all":true,"apply":true,"limit":4}`)
	if payload["repaired"] != float64(4) {
		t.Fatalf("repaired=%v want 4", payload["repaired"])
	}
	if payload["remaining"] != float64(6) {
		t.Fatalf("remaining=%v want 6", payload["remaining"])
	}
	if len(store.revoked) != 4 {
		t.Fatalf("revoked=%v", store.revoked)
	}
}

func TestCommercialBaselineRepairKeepsAccountsWithPaidOrManualQuota(t *testing.T) {
	paid := baselineRepairCandidate(1598, map[string]float64{"MiniMax-M2.7": 1000})
	paid.PaidActive = 1
	manual := baselineRepairCandidate(1599, map[string]float64{"MiniMax-M2.7": 1000})
	manual.ManualGrants = 1

	store := newCommercialBaselineRepairStore()
	store.candidates = []*types.CommercialRelayBaselineCandidate{paid, manual}
	payload := runCommercialBaselineRepair(t, store, `{"all":true,"apply":true}`)
	if len(store.revoked) != 0 {
		t.Fatalf("revoked=%v want none", store.revoked)
	}
	if payload["repaired"] != float64(0) || payload["skipped"] != float64(2) {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestCommercialBaselineRepairHonoursExplicitUIDs(t *testing.T) {
	store := newCommercialBaselineRepairStore()
	store.candidates = []*types.CommercialRelayBaselineCandidate{
		baselineRepairCandidate(1601, map[string]float64{"MiniMax-M2.7": 1000}),
		baselineRepairCandidate(1602, map[string]float64{"MiniMax-M2.7": 1000}),
	}
	runCommercialBaselineRepair(t, store, `{"uids":[1602],"apply":true}`)
	if len(store.revoked) != 1 || store.revoked[0] != 1602 {
		t.Fatalf("revoked=%v want [1602]", store.revoked)
	}
}

func TestCommercialBaselineRepairRequiresASelection(t *testing.T) {
	store := newCommercialBaselineRepairStore()
	handler := &AccountAdminHandler{commercial: store}
	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/baseline-repair", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.HandleCommercialBaselineRepair(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}
