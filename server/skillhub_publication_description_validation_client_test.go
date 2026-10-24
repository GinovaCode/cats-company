package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func descriptionDerivedReport(app store.SkillHubPublicationDescriptionApplication) PublicationValidationReport {
	report := publicationReportFixture(app.Input.Receipt).Validation
	report.Outcome, report.NextAction = "passed", "none"
	report.CandidateHash, report.LimitsFingerprint = app.Result.Derivation.DerivedCandidateHash, app.Result.Derivation.LimitsFingerprint
	report.Issues = []PublicationValidationIssue{}
	return report
}

func descriptionValidationResponse(app store.SkillHubPublicationDescriptionApplication) map[string]any {
	out := map[string]any{"schemaVersion": 1, "applicationId": app.ID, "taskId": app.Input.Receipt.TaskID,
		"derivedBytes": app.Result.RequestBytes, "receipt": app.Result.Derivation, "validation": descriptionDerivedReport(app)}
	// Mutable wire representation, including nested objects, for corruption cases.
	raw, _ := json.Marshal(out)
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestPublicationDescriptionValidationClientDefaultOff(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	for _, master := range []bool{false, true} {
		cfg := derivationClientConfig(s)
		cfg.Enabled, cfg.ValidationEnabled = master, !master
		c, err := NewPublicationDescriptionDerivationClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if report, err := c.ReadValidationReport(context.Background(), store.SkillHubPublicationDescriptionApplication{}); report != nil || err != ErrSkillHubPublicationDescriptionValidationDisabled {
			t.Fatal("disabled report dispatched", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("disabled report reached peer")
	}
	cfg := derivationClientConfig(s)
	for key, value := range map[string]string{"ENABLED": "true", "URL": cfg.URL, "ISSUER": cfg.Issuer, "AUDIENCE": cfg.Audience, "PEER_SECRET": cfg.PeerSecret, "TIMEOUT_MS": "1000"} {
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_"+key, value)
	}
	for _, value := range []string{"", "false", "TRUE", " 1 "} {
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_VALIDATION_ENABLED", value)
		c, err := NewPublicationDescriptionDerivationClientFromEnv()
		if err != nil || c.validationEnabled != (value == "TRUE" || strings.TrimSpace(value) == "1") {
			t.Fatal(value, err)
		}
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED", "false")
	c, err := NewPublicationDescriptionDerivationClientFromEnv()
	if err != nil || c.validationEnabled {
		t.Fatal("validation bypassed master gate", err)
	}
}

func TestPublicationDescriptionValidationClientExactHTTPSContract(t *testing.T) {
	var app store.SkillHubPublicationDescriptionApplication
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.URL.Path != publicationDescriptionValidationPath || r.Header.Get("Authorization") != "Bearer pubderive_"+strings.Repeat("a", 64) ||
			r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("invalid peer request")
		}
		raw, _ := io.ReadAll(r.Body)
		var input map[string]json.RawMessage
		if json.Unmarshal(raw, &input) != nil || len(input) != 8 {
			t.Error("invalid request shape")
		}
		want := map[string]any{"schemaVersion": 1, "applicationId": app.ID, "taskId": app.Input.Receipt.TaskID,
			"requestHash": app.Input.Receipt.Query.RequestHash, "candidateHash": app.Input.Draft.Evidence.CandidateHash,
			"proposal": app.Input.Draft.Proposal, "expectedReceipt": app.Result.Derivation, "derivedBytes": app.Result.RequestBytes}
		for key, value := range want {
			encoded, _ := json.Marshal(value)
			if string(input[key]) != string(encoded) {
				t.Errorf("wrong %s", key)
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(descriptionValidationResponse(app))
	}))
	defer s.Close()
	app = completedDescriptionApplication(t, s.URL, []byte("private-copy"))
	cfg := derivationClientConfig(s)
	cfg.ValidationEnabled = true // Octet read gate deliberately remains false.
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(s.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "must-not-leak"}})
	cfg.HTTPClient.Jar = jar
	for i := 0; i < 2; i++ {
		c, err := NewPublicationDescriptionDerivationClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		report, err := c.ReadValidationReport(context.Background(), app)
		if err != nil || report == nil || !reflect.DeepEqual(*report, descriptionDerivedReport(app)) {
			t.Fatal("report mismatch", err)
		}
	}
	if requests.Load() != 2 || cfg.HTTPClient.Jar != jar {
		t.Fatal("retried or mutated caller client")
	}
}

func TestPublicationDescriptionValidationClientRejectsInputs(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	cfg := derivationClientConfig(s)
	cfg.ValidationEnabled = true
	c, _ := NewPublicationDescriptionDerivationClient(cfg)
	for _, mode := range []string{"incomplete", "identity", "receipt", "proposal", "issuer", "audience"} {
		t.Run(mode, func(t *testing.T) {
			app := completedDescriptionApplication(t, s.URL, []byte("private-copy"))
			switch mode {
			case "incomplete":
				app.Result = nil
			case "identity":
				app.ID += "0"
			case "receipt":
				app.Result.Derivation.OriginalRequestHash = strings.Repeat("f", 64)
			case "proposal":
				app.Result.Derivation.ProposalDigest = strings.Repeat("f", 64)
			case "issuer":
				app.Input.Receipt.Query.Issuer = "https://wrong.example"
			case "audience":
				app.Input.Receipt.Query.Audience = "https://wrong.example"
			}
			if report, err := c.ReadValidationReport(context.Background(), app); report != nil || err != ErrSkillHubPublicationDescriptionDerivationInput {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report, err := c.ReadValidationReport(ctx, completedDescriptionApplication(t, s.URL, []byte("private"))); report != nil || err != context.Canceled {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input dispatched")
	}
}

func TestPublicationDescriptionValidationClientRejectsMalformedReports(t *testing.T) {
	for _, mode := range []string{"app", "task", "size", "receipt", "candidate", "limits", "truncated", "rejected", "coverage", "missing", "null", "unknown", "duplicate", "nested-duplicate", "private-path", "oversized", "status", "type", "encoding", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var app store.SkillHubPublicationDescriptionApplication
			var requests atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				out := descriptionValidationResponse(app)
				report := out["validation"].(map[string]any)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				switch mode {
				case "app":
					out["applicationId"] = "pubdesc_" + strings.Repeat("f", 64)
				case "task":
					out["taskId"] = "pubtask_other"
				case "size":
					out["derivedBytes"] = app.Result.RequestBytes + 1
				case "receipt":
					out["receipt"].(map[string]any)["proposalDigest"] = strings.Repeat("f", 64)
				case "candidate":
					report["candidateHash"] = app.Input.Receipt.Query.SourceSnapshotHash
				case "limits":
					report["limitsFingerprint"] = strings.Repeat("f", 64)
				case "truncated":
					report["truncated"] = true
				case "rejected":
					report["outcome"], report["nextAction"], report["truncated"] = "rejected", "review_report", true
				case "coverage":
					report["coverage"] = "partial"
				case "missing":
					delete(report, "truncated")
				case "null":
					report["truncated"] = nil
				case "unknown":
					out["sourceBytes"] = "PRIVATE"
				case "private-path":
					report["issues"] = []any{map[string]any{"code": "metadata.warning", "category": "compatibility", "blocking": false, "repairPolicy": "none", "message": "warning", "location": map[string]any{"relativePath": "private/file.txt"}}}
				case "status":
					w.WriteHeader(409)
				case "type":
					w.Header().Set("Content-Type", "application/octet-stream")
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "redirect":
					w.Header().Set("Location", "/must-not-follow")
					w.WriteHeader(307)
				}
				raw, _ := json.Marshal(out)
				if mode == "oversized" {
					raw = []byte(strings.Repeat(" ", 64*1024+1))
				}
				if mode == "duplicate" {
					raw = append([]byte(`{"schemaVersion":1,`), raw[1:]...)
				}
				if mode == "nested-duplicate" {
					raw = []byte(strings.Replace(string(raw), `"truncated":false`, `"truncated":false,"truncated":false`, 1))
				}
				_, _ = w.Write(raw)
			}))
			defer s.Close()
			app = completedDescriptionApplication(t, s.URL, []byte("private-copy"))
			cfg := derivationClientConfig(s)
			cfg.ValidationEnabled = true
			cfg.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("followed redirect"); return nil }
			c, _ := NewPublicationDescriptionDerivationClient(cfg)
			if report, err := c.ReadValidationReport(context.Background(), app); report != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionDerivationPeer) || requests.Load() != 1 {
				t.Fatal("unsafe report accepted/retried", err)
			}
		})
	}
}

func TestPublicationDescriptionValidationClientCancellation(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	cfg := derivationClientConfig(s)
	cfg.ValidationEnabled = true
	c, err := NewPublicationDescriptionDerivationClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	report, err := c.ReadValidationReport(ctx, completedDescriptionApplication(t, s.URL, []byte("private")))
	if report != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancelled peer returned a report", err)
	}
}
