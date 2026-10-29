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

func descriptionPlanFixture(app store.SkillHubPublicationDescriptionApplication, confirm bool) publicationDescriptionReleasePlan {
	target := commitClientTarget(app.Input.Receipt)
	target.CandidateHash, target.ConfirmVersionPublish = app.Result.Derivation.DerivedCandidateHash, confirm
	return publicationDescriptionReleasePlan{1, app.ID, app.Input.Receipt.TaskID, publicationControlReceipt(app.Input.Receipt), app.Result.Derivation, app.Result.RequestBytes, target}
}

func TestPublicationDescriptionReleasePlanClientDefaultOff(t *testing.T) {
	for _, master := range []bool{false, true} {
		c, err := NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{Enabled: false, PlanEnabled: master})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := c.PlanRetainedCopy(context.Background(), store.SkillHubPublicationDescriptionApplication{}, true); got != nil || err != ErrSkillHubPublicationDescriptionPlanDisabled {
			t.Fatal("master gate bypassed", err)
		}
	}
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("disabled peer called") }))
	defer s.Close()
	c, _ := NewPublicationDescriptionDerivationClient(derivationClientConfig(s))
	if got, err := c.PlanRetainedCopy(context.Background(), store.SkillHubPublicationDescriptionApplication{}, false); got != nil || err != ErrSkillHubPublicationDescriptionPlanDisabled {
		t.Fatal(err)
	}
	cfg := derivationClientConfig(s)
	for key, value := range map[string]string{"ENABLED": "true", "URL": cfg.URL, "ISSUER": cfg.Issuer, "AUDIENCE": cfg.Audience, "PEER_SECRET": cfg.PeerSecret, "TIMEOUT_MS": "1000"} {
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_"+key, value)
	}
	for _, value := range []string{"", "false", "TRUE", "1"} {
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_PLAN_ENABLED", value)
		c, err := NewPublicationDescriptionDerivationClientFromEnv()
		if err != nil || c.planEnabled != (value == "TRUE" || value == "1") {
			t.Fatal(value, err)
		}
	}
}

func TestPublicationDescriptionReleasePlanClientHTTPS(t *testing.T) {
	var app store.SkillHubPublicationDescriptionApplication
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.URL.Path != publicationDescriptionPlanPath || r.Header.Get("Authorization") != "Bearer pubderive_"+strings.Repeat("a", 64) ||
			r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("invalid service request")
		}
		raw, _ := io.ReadAll(r.Body)
		parts, err := publicationExactObject(raw, "copy", "publicationReceipt", "confirmVersionPublish")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var receipt publicationCloudReceiptRequest
		if json.Unmarshal(parts["publicationReceipt"], &receipt) != nil || receipt != publicationControlReceipt(app.Input.Receipt) || string(parts["confirmVersionPublish"]) != "false" {
			t.Error("original receipt/version policy changed")
		}
		copy, err := publicationExactObject(parts["copy"], "schemaVersion", "applicationId", "taskId", "requestHash", "candidateHash", "proposal", "expectedReceipt", "derivedBytes")
		if err != nil || string(copy["candidateHash"]) != `"`+app.Input.Draft.Evidence.CandidateHash+`"` || string(copy["applicationId"]) != `"`+app.ID+`"` {
			t.Error("copy binding changed")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(descriptionPlanFixture(app, false))
	}))
	defer s.Close()
	app = completedDescriptionApplication(t, s.URL, []byte("private-copy"))
	cfg := derivationClientConfig(s)
	cfg.PlanEnabled = true // Read and validation gates deliberately stay off.
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(s.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "private"}})
	cfg.HTTPClient.Jar = jar
	for i := 0; i < 2; i++ {
		c, err := NewPublicationDescriptionDerivationClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.PlanRetainedCopy(context.Background(), app, false)
		if err != nil || got == nil || *got != descriptionPlanFixture(app, false) {
			t.Fatal("bad plan/reconstruction", err)
		}
	}
	if requests.Load() != 2 || cfg.HTTPClient.Jar != jar {
		t.Fatal("unexpected retry or caller mutation")
	}
}

func TestPublicationDescriptionReleasePlanClientRejectsInputs(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	cfg := derivationClientConfig(s)
	cfg.PlanEnabled = true
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
			if got, err := c.PlanRetainedCopy(context.Background(), app, false); got != nil || err != ErrSkillHubPublicationDescriptionDerivationInput {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := c.PlanRetainedCopy(ctx, completedDescriptionApplication(t, s.URL, []byte("private")), false); got != nil || err != context.Canceled {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input dispatched")
	}
}

func TestPublicationDescriptionReleasePlanClientRejectsSubstitution(t *testing.T) {
	for _, mode := range []string{"app", "task", "size", "receipt", "original-receipt", "candidate", "version-confirmation", "target", "missing-bool", "null-bool", "unknown", "missing", "null", "duplicate", "nested-duplicate", "oversized", "status", "type", "encoding", "cache", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var app store.SkillHubPublicationDescriptionApplication
			var requests atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				raw, _ := json.Marshal(descriptionPlanFixture(app, false))
				var out map[string]any
				_ = json.Unmarshal(raw, &out)
				target := out["target"].(map[string]any)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				switch mode {
				case "app":
					out["applicationId"] = "pubdesc_" + strings.Repeat("f", 64)
				case "task":
					out["taskId"] = "pubtask_other"
				case "size":
					out["derivedBytes"] = app.Result.RequestBytes + 1
				case "receipt":
					out["receipt"].(map[string]any)["proposalDigest"] = strings.Repeat("f", 64)
				case "original-receipt":
					out["publicationReceipt"].(map[string]any)["sourceSnapshotHash"] = app.Result.Derivation.DerivedCandidateHash
				case "candidate":
					target["candidateHash"] = app.Input.Receipt.Query.SourceSnapshotHash
				case "version-confirmation":
					target["confirmVersionPublish"] = true
				case "target":
					target["skillId"] = "different/skill"
				case "missing-bool":
					delete(target, "confirmVersionPublish")
				case "null-bool":
					target["confirmVersionPublish"] = nil
				case "unknown":
					out["privateBytes"] = "SECRET"
				case "missing":
					delete(out, "receipt")
				case "null":
					out["schemaVersion"] = nil
				case "status":
					w.WriteHeader(409)
				case "type":
					w.Header().Set("Content-Type", "application/octet-stream")
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "cache":
					w.Header().Del("Cache-Control")
				case "redirect":
					w.Header().Set("Location", "/unexpected")
					w.WriteHeader(307)
				}
				raw, _ = json.Marshal(out)
				if mode == "duplicate" {
					raw = append([]byte(`{"schemaVersion":1,`), raw[1:]...)
				}
				if mode == "nested-duplicate" {
					raw = []byte(strings.Replace(string(raw), `"confirmVersionPublish":false`, `"confirmVersionPublish":false,"confirmVersionPublish":false`, 1))
				}
				if mode == "oversized" {
					raw = []byte(strings.Repeat(" ", 16385))
				}
				_, _ = w.Write(raw)
			}))
			defer s.Close()
			app = completedDescriptionApplication(t, s.URL, []byte("private"))
			cfg := derivationClientConfig(s)
			cfg.PlanEnabled = true
			c, _ := NewPublicationDescriptionDerivationClient(cfg)
			got, err := c.PlanRetainedCopy(context.Background(), app, false)
			if got != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionDerivationPeer) || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("substituted reply accepted", mode, err)
			}
			if requests.Load() != 1 {
				t.Fatal("redirect/retry dispatched")
			}
		})
	}
}

func TestPublicationDescriptionReleasePlanClientVersionPolicy(t *testing.T) {
	for _, status := range []int{409, 503} {
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"code":"skillhub.publication_description_derivation_version_confirmation_required","message":"PRIVATE upstream"}}`)
		}))
		app := completedDescriptionApplication(t, s.URL, []byte("private"))
		cfg := derivationClientConfig(s)
		cfg.PlanEnabled = true
		c, _ := NewPublicationDescriptionDerivationClient(cfg)
		got, err := c.PlanRetainedCopy(context.Background(), app, false)
		s.Close()
		want := ErrSkillHubPublicationDescriptionDerivationPeer
		if status == 409 {
			want = errPublicationCommitConfirmationRequired
		}
		if got != nil || err != want || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal(err)
		}
	}
}

func TestPublicationDescriptionReleasePlanClientOwnsInput(t *testing.T) {
	// The configured test transport mutates caller-owned pointers during dispatch.
	// Verification must still bind the snapshotted application, not that mutation.
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer s.Close()
	app := completedDescriptionApplication(t, s.URL, []byte("private"))
	before := snapshotPublicationDescriptionApplication(app)
	cfg := derivationClientConfig(s)
	cfg.PlanEnabled = true
	cfg.HTTPClient = &http.Client{Transport: descriptionPlanRoundTripper(func(r *http.Request) (*http.Response, error) {
		*app.Input.Draft.Proposal.EntryIndex = 99
		app.Result.RequestBytes++
		raw, _ := json.Marshal(descriptionPlanFixture(before, false))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}}, Body: io.NopCloser(strings.NewReader(string(raw))), ContentLength: int64(len(raw))}, nil
	})}
	c, _ := NewPublicationDescriptionDerivationClient(cfg)
	got, err := c.PlanRetainedCopy(context.Background(), app, false)
	if err != nil || got == nil || !reflect.DeepEqual(*got, descriptionPlanFixture(before, false)) {
		t.Fatal(err)
	}
}

type descriptionPlanRoundTripper func(*http.Request) (*http.Response, error)

func (f descriptionPlanRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicationDescriptionReleasePlanClientHoldsCapacityUntilSettlement(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer s.Close()
	app := completedDescriptionApplication(t, s.URL, []byte("private"))
	cfg := derivationClientConfig(s)
	cfg.PlanEnabled = true
	started, release := make(chan struct{}, 2), make(chan struct{})
	cfg.HTTPClient = &http.Client{Transport: descriptionPlanRoundTripper(func(*http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release // Transport can settle later than caller cancellation.
		return nil, errors.New("PRIVATE transport error")
	})}
	c, _ := NewPublicationDescriptionDerivationClient(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := c.PlanRetainedCopy(ctx, app, false); done <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("request never dispatched")
		}
	}
	cancel()
	got, err := c.PlanRetainedCopy(context.Background(), app, false)
	close(release)
	if got != nil || err != store.ErrSkillHubPublicationBusy {
		t.Fatal("cancelled transport released capacity early", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Fatal("private transport error escaped", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("transport did not settle")
		}
	}
}
