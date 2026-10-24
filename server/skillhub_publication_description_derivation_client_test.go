package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func clientDescriptionApplication(t *testing.T, audience string, proposal *store.SkillHubPublicationDescriptionProposal) store.SkillHubPublicationDescriptionApplication {
	t.Helper()
	db, _ := applicationTestFixture(t)
	app := *db.app
	app.Input.Receipt.Query.Audience = audience
	if proposal != nil {
		app.Input.Draft.Proposal = *proposal
	}
	taskInput := store.SkillHubPublicationRepairInput{Receipt: app.Input.Receipt, TopicID: "grp_123", Evidence: app.Input.Draft.Evidence}
	taskHash, err := taskInput.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.Input.Draft.TaskIdentityHash = taskHash
	round := store.SkillHubPublicationRepairRoundInput{RequestID: "description-round-1", Evidence: taskInput.Evidence}
	app.Input.Draft.RoundIdentityHash, err = round.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.Input.ConfirmationKey, err = app.Input.Draft.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.IdentityHash, err = app.Input.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	app.ID = "pubdesc_" + app.IdentityHash
	confirmed := app.CreatedAt.Add(-time.Millisecond)
	task := store.SkillHubPublicationRepairTask{ID: app.Input.Draft.TaskID, Input: taskInput, IdentityHash: taskHash, CreatedAt: confirmed.Add(-time.Second),
		Rounds: []store.SkillHubPublicationRepairRound{{Lease: store.SkillHubPublicationRepairLease{
			EntryID: app.Input.Receipt.Query.EntryID, TaskID: app.Input.Draft.TaskID, Round: 1, Input: round,
			WorkerID: "description-worker", Token: strings.Repeat("e", 32), Generation: 1, Until: confirmed.Add(time.Minute),
		}, Result: &store.SkillHubPublicationRepairResult{Outcome: "waiting_confirmation", Evidence: taskInput.Evidence}}}}
	draft := store.SkillHubPublicationDescriptionDraft{EntryID: app.Input.Receipt.Query.EntryID, Input: app.Input.Draft,
		ConfirmationKey: app.Input.ConfirmationKey, CreatedAt: confirmed.Add(-time.Second), ConfirmedAt: &confirmed}
	if err := app.ValidateFor(task, draft); err != nil {
		t.Fatalf("invalid application fixture: %v", err)
	}
	return app
}

func derivationClientConfig(s *httptest.Server) PublicationDescriptionDerivationClientConfig {
	return PublicationDescriptionDerivationClientConfig{Enabled: true, URL: s.URL + publicationDescriptionDerivationPath,
		Issuer: "https://cats.example", Audience: s.URL, PeerSecret: "pubderive_" + strings.Repeat("a", 64), Timeout: 5 * time.Second, HTTPClient: s.Client()}
}

func derivationResponse(app store.SkillHubPublicationDescriptionApplication) map[string]any {
	return map[string]any{"applicationId": app.ID, "taskId": app.Input.Receipt.TaskID, "derivedBytes": 101,
		"derivedRequestHash": strings.Repeat("8", 64), "derivedCandidateHash": strings.Repeat("9", 64), "replayed": false,
		"receipt": store.SkillHubPublicationDescriptionDerivationReceipt{SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
			OriginalRequestHash: app.Input.Receipt.Query.RequestHash, OriginalCandidateHash: app.Input.Draft.Evidence.CandidateHash,
			ValidationDigest: strings.Repeat("5", 64), ProposalDigest: publicationDescriptionProposalDigest(app.Input.Draft.Proposal),
			LimitsFingerprint: strings.Repeat("7", 64), DerivedRequestHash: strings.Repeat("8", 64), DerivedCandidateHash: strings.Repeat("9", 64)}}
}

func TestPublicationDescriptionDerivationClientDefaultOff(t *testing.T) {
	c, err := NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeriveAndRetain(context.Background(), store.SkillHubPublicationDescriptionApplication{}); err != ErrSkillHubPublicationDescriptionDerivationDisabled {
		t.Fatal(err)
	}
}

func TestPublicationDescriptionDerivationClientStrictPeerContract(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "replay"}[replay], func(t *testing.T) {
			var app store.SkillHubPublicationDescriptionApplication
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != publicationDescriptionDerivationPath || r.Header.Get("Authorization") != "Bearer pubderive_"+strings.Repeat("a", 64) {
					t.Error("unexpected request endpoint/authentication")
				}
				if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
					t.Error("browser credentials leaked")
				}
				var body struct {
					SchemaVersion int                                          `json:"schemaVersion"`
					ApplicationID string                                       `json:"applicationId"`
					TaskID        string                                       `json:"taskId"`
					RequestHash   string                                       `json:"requestHash"`
					CandidateHash string                                       `json:"candidateHash"`
					Proposal      store.SkillHubPublicationDescriptionProposal `json:"proposal"`
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if err := decodeUniqueJSON(raw, &body); err != nil {
					t.Error(err)
					return
				}
				if body.SchemaVersion != 1 || body.ApplicationID != app.ID || body.TaskID != app.Input.Receipt.TaskID || body.RequestHash != app.Input.Receipt.Query.RequestHash || body.CandidateHash != app.Input.Draft.Evidence.CandidateHash || publicationDescriptionProposalDigest(body.Proposal) != publicationDescriptionProposalDigest(app.Input.Draft.Proposal) {
					t.Error("request binding mismatch")
				}
				out := derivationResponse(app)
				out["replayed"] = replay
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer s.Close()
			app = clientDescriptionApplication(t, s.URL, nil)
			c, err := NewPublicationDescriptionDerivationClient(derivationClientConfig(s))
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.DeriveAndRetain(context.Background(), app)
			if err != nil {
				t.Fatal(err)
			}
			if result.ValidateFor(app) != nil || result.RequestBytes != 101 {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
}

func TestPublicationDescriptionDerivationClientRejectsRedirects(t *testing.T) {
	for _, injected := range []bool{false, true} {
		for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			t.Run(strings.Join([]string{map[bool]string{false: "default", true: "injected"}[injected], http.StatusText(status)}, "/"), func(t *testing.T) {
				var targets, callbacks atomic.Int32
				target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targets.Add(1) }))
				defer target.Close()
				s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, status) }))
				defer s.Close()
				cfg := derivationClientConfig(s)
				if !injected {
					cfg.HTTPClient = nil
				} else {
					cfg.HTTPClient.Timeout = 90 * time.Second
					cfg.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { callbacks.Add(1); return nil }
				}
				c, err := NewPublicationDescriptionDerivationClient(cfg)
				if err != nil {
					t.Fatal(err)
				}
				// Test trust is installed on the private client's transport only.
				if !injected {
					c.client.Transport = s.Client().Transport
				}
				_, err = c.DeriveAndRetain(context.Background(), clientDescriptionApplication(t, s.URL, nil))
				if !errors.Is(err, ErrSkillHubPublicationDescriptionDerivationPeer) || targets.Load() != 0 || callbacks.Load() != 0 {
					t.Fatalf("redirect escaped: err=%v targets=%d callbacks=%d", err, targets.Load(), callbacks.Load())
				}
				if injected && (cfg.HTTPClient.Timeout != 90*time.Second || cfg.HTTPClient.CheckRedirect == nil || cfg.HTTPClient == c.client) {
					t.Fatal("caller client mutated")
				}
			})
		}
	}
}

func TestPublicationDescriptionDerivationClientRejectsInputsBeforeDispatch(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	c, err := NewPublicationDescriptionDerivationClient(derivationClientConfig(s))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"issuer", "audience", "identity", "confirmation"} {
		t.Run(field, func(t *testing.T) {
			app := clientDescriptionApplication(t, s.URL, nil)
			switch field {
			case "issuer":
				app.Input.Receipt.Query.Issuer = "https://other.example"
			case "audience":
				app.Input.Receipt.Query.Audience = "https://other.example"
			case "identity":
				app.ID = "pubdesc_" + strings.Repeat("a", 64)
			case "confirmation":
				app.Input.ConfirmationKey = ""
			}
			// Retain a self-consistent identity even for another issuer/audience.
			if field == "issuer" || field == "audience" {
				app.IdentityHash, _ = app.Input.IdentityHash()
				app.ID = "pubdesc_" + app.IdentityHash
			}
			if _, err := c.DeriveAndRetain(context.Background(), app); err != ErrSkillHubPublicationDescriptionDerivationInput {
				t.Fatal(err)
			}
		})
	}
	p := store.SkillHubPublicationDescriptionProposal{RelativePath: "skill.json", Field: "description", Text: "Read docs."}
	app := clientDescriptionApplication(t, s.URL, &p)
	if _, err := c.DeriveAndRetain(context.Background(), app); err != ErrSkillHubPublicationDescriptionDerivationUnsupported {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.DeriveAndRetain(ctx, clientDescriptionApplication(t, s.URL, nil)); err != context.Canceled {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input dispatched")
	}
}

func TestPublicationDescriptionDerivationClientRejectsPeerResponses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		raw    func([]byte) []byte
		status int
	}{
		{name: "unknown", mutate: func(m map[string]any) { m["unknown"] = true }},
		{name: "duplicate", raw: func(b []byte) []byte { return append([]byte(`{"replayed":true,`), b[1:]...) }},
		{name: "oversized", raw: func([]byte) []byte { return []byte(strings.Repeat(" ", 64*1024+1)) }},
		{name: "status", status: 503},
		{name: "zero bytes", mutate: func(m map[string]any) { m["derivedBytes"] = 0 }},
		{name: "excess bytes", mutate: func(m map[string]any) { m["derivedBytes"] = store.SkillHubPublicationCaptureMaxBytes + 1 }},
		{name: "request hash mismatch", mutate: func(m map[string]any) { m["derivedRequestHash"] = strings.Repeat("0", 64) }},
		{name: "candidate hash mismatch", mutate: func(m map[string]any) { m["derivedCandidateHash"] = strings.Repeat("0", 64) }},
		{name: "missing replay", mutate: func(m map[string]any) { delete(m, "replayed") }},
		{name: "null replay", mutate: func(m map[string]any) { m["replayed"] = nil }},
		{name: "application mismatch", mutate: func(m map[string]any) { m["applicationId"] = "pubdesc_" + strings.Repeat("0", 64) }},
		{name: "task mismatch", mutate: func(m map[string]any) { m["taskId"] = "pubtask_other" }},
		{name: "proposal digest mismatch", mutate: func(m map[string]any) {
			r := m["receipt"].(store.SkillHubPublicationDescriptionDerivationReceipt)
			r.ProposalDigest = strings.Repeat("0", 64)
			m["receipt"] = r
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var app store.SkillHubPublicationDescriptionApplication
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				out := derivationResponse(app)
				if tc.mutate != nil {
					tc.mutate(out)
				}
				b, _ := json.Marshal(out)
				if tc.raw != nil {
					b = tc.raw(b)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write(b)
			}))
			defer s.Close()
			app = clientDescriptionApplication(t, s.URL, nil)
			c, err := NewPublicationDescriptionDerivationClient(derivationClientConfig(s))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.DeriveAndRetain(context.Background(), app); !errors.Is(err, ErrSkillHubPublicationDescriptionDerivationPeer) {
				t.Fatalf("response accepted: %v", err)
			}
		})
	}
}

type derivationRoundTripper func(*http.Request) (*http.Response, error)

func (f derivationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicationDescriptionDerivationClientTimeout(t *testing.T) {
	cfg := PublicationDescriptionDerivationClientConfig{Enabled: true, URL: "https://hub.example" + publicationDescriptionDerivationPath, Issuer: "https://cats.example", Audience: "https://hub.example", PeerSecret: "pubderive_" + strings.Repeat("a", 64), Timeout: time.Second,
		HTTPClient: &http.Client{Transport: derivationRoundTripper(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}}
	c, err := NewPublicationDescriptionDerivationClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DeriveAndRetain(context.Background(), clientDescriptionApplication(t, cfg.Audience, nil))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not propagated: %v", err)
	}
}

func TestPublicationDescriptionProposalDigestCloudVectors(t *testing.T) {
	// Fixed vectors from Cloud's JSON.stringify canonical proposal format.
	index := int64(0)
	for _, tc := range []struct{ text, digest string }{
		{"Read a document.", "c8ad840b7b569f02f8da5b97c9548a9f021515d0a83d823965f699e61acbb1c3"},
		{"Search <docs> & replace > files.", "f0a25f197f0f6b1adfbd54af3f046b506f5c4e5c0eba41503da1dd1b222ae6a7"},
		{"中文😀\u2028下一行\u2029结束", "95b7e3dad2669835ff79871a17ea88ff88fd9956d3a06e22743b9ae19d1ced8a"},
		{`literal \u2028 and \u003c`, "8fd3ba921619a56eccfad0f9cbd9136ef10ebf7db1ffd4b0977a0e7324f50272"},
		{"quote \" slash \\ newline\n\r\t", "90b19bb85f6a30beab98e656195a4bfc0a07d72ba654ca7294d799bdf04d2560"},
	} {
		p := store.SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &index, Text: tc.text}
		if got := publicationDescriptionProposalDigest(p); got != tc.digest {
			t.Errorf("digest %q = %s, want %s", tc.text, got, tc.digest)
		}
	}
	p := store.SkillHubPublicationDescriptionProposal{RelativePath: "skill.json", Field: "description", Text: "Read docs."}
	if publicationDescriptionProposalDigest(p) == "" {
		t.Fatal("nil selector digest")
	}
	if got := cloudDescriptionJSONString("\x00\b\f\x1f"); got != `"\u0000\b\f\u001f"` {
		t.Fatalf("control quoting: %s", got)
	}
}

func TestPublicationDescriptionDerivationClientFromEnv(t *testing.T) {
	const prefix = "CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_"
	for _, key := range []string{"ENABLED", "URL", "ISSUER", "AUDIENCE", "PEER_SECRET", "TIMEOUT_MS"} {
		t.Setenv(prefix+key, "")
	}
	t.Setenv(prefix+"TIMEOUT_MS", "invalid")
	c, err := NewPublicationDescriptionDerivationClientFromEnv()
	if err != nil || c.enabled {
		t.Fatalf("disabled config: %v", err)
	}
	t.Setenv(prefix+"URL", "https://hub.example"+publicationDescriptionDerivationPath)
	t.Setenv(prefix+"ISSUER", "https://cats.example")
	t.Setenv(prefix+"AUDIENCE", "https://hub.example")
	t.Setenv(prefix+"PEER_SECRET", "pubderive_"+strings.Repeat("a", 64))
	for _, enabled := range []string{" 1 ", " TRUE "} {
		t.Setenv(prefix+"ENABLED", enabled)
		for _, raw := range []string{"invalid", "999", "60001", "9223372036854775807"} {
			t.Setenv(prefix+"TIMEOUT_MS", raw)
			if _, err := NewPublicationDescriptionDerivationClientFromEnv(); err != ErrSkillHubPublicationDescriptionDerivationConfig {
				t.Fatalf("accepted timeout %q: %v", raw, err)
			}
		}
		t.Setenv(prefix+"TIMEOUT_MS", "")
		c, err = NewPublicationDescriptionDerivationClientFromEnv()
		if err != nil || !c.enabled || c.client.Timeout != 15*time.Second {
			t.Fatalf("enabled config: %v", err)
		}
	}
}

func TestPublicationDescriptionDerivationClientConfig(t *testing.T) {
	valid := PublicationDescriptionDerivationClientConfig{Enabled: true, URL: "https://hub.example" + publicationDescriptionDerivationPath, Issuer: "https://cats.example", Audience: "https://hub.example", PeerSecret: "pubderive_" + strings.Repeat("a", 64), Timeout: time.Second}
	for _, mutate := range []func(*PublicationDescriptionDerivationClientConfig){
		func(c *PublicationDescriptionDerivationClientConfig) {
			c.URL = "http://hub.example" + publicationDescriptionDerivationPath
		},
		func(c *PublicationDescriptionDerivationClientConfig) { c.URL += "?query=1" },
		func(c *PublicationDescriptionDerivationClientConfig) {
			c.URL = "https://user@hub.example" + publicationDescriptionDerivationPath
		},
		func(c *PublicationDescriptionDerivationClientConfig) { c.Audience = "https://other.example" },
		func(c *PublicationDescriptionDerivationClientConfig) { c.Issuer += "/path" },
		func(c *PublicationDescriptionDerivationClientConfig) {
			c.PeerSecret = "pubderive_" + strings.Repeat("A", 64)
		},
		func(c *PublicationDescriptionDerivationClientConfig) { c.Timeout = time.Minute + time.Second },
	} {
		cfg := valid
		mutate(&cfg)
		if _, err := NewPublicationDescriptionDerivationClient(cfg); err != ErrSkillHubPublicationDescriptionDerivationConfig {
			t.Fatal("invalid configuration accepted", err)
		}
	}
}
