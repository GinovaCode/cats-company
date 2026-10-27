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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Synthetic retained peer bytes. The application/draft identity is validated by
// the shared fixture; this is a TLS wire contract, not a Cloud derivation E2E.
func completedDescriptionApplication(t *testing.T, audience string, raw []byte) store.SkillHubPublicationDescriptionApplication {
	t.Helper()
	app := clientDescriptionApplication(t, audience, nil)
	app.Result = &store.SkillHubPublicationDescriptionApplicationResult{ApplicationID: app.ID, RequestBytes: int64(len(raw)),
		Derivation: store.SkillHubPublicationDescriptionDerivationReceipt{SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
			OriginalRequestHash: app.Input.Receipt.Query.RequestHash, OriginalCandidateHash: app.Input.Draft.Evidence.CandidateHash,
			ValidationDigest: strings.Repeat("5", 64), ProposalDigest: publicationDescriptionProposalDigest(app.Input.Draft.Proposal),
			LimitsFingerprint: strings.Repeat("7", 64), DerivedRequestHash: publicationDescriptionSHA256Hex(string(raw)), DerivedCandidateHash: strings.Repeat("9", 64)}}
	applied := app.CreatedAt.Add(time.Millisecond)
	app.AppliedAt = &applied
	if err := validatePublicationDescriptionCopyApplication(app); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestPublicationDescriptionCopyClientDefaultOff(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	for _, mainEnabled := range []bool{false, true} {
		cfg := derivationClientConfig(s)
		cfg.Enabled = mainEnabled
		cfg.ReadEnabled = !mainEnabled
		c, err := NewPublicationDescriptionDerivationClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if raw, err := c.ReadRetainedCopy(context.Background(), completedDescriptionApplication(t, s.URL, []byte("private"))); raw != nil || err != ErrSkillHubPublicationDescriptionReadDisabled {
			t.Fatalf("disabled read: %v %v", raw, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("disabled read dispatched")
	}
	const flag = "CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_READ_ENABLED"
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED", "false")
	t.Setenv(flag, "true")
	c, err := NewPublicationDescriptionDerivationClientFromEnv()
	if err != nil || c.readEnabled {
		t.Fatalf("read flag bypassed master gate: %v", err)
	}
	cfg := derivationClientConfig(s)
	for key, value := range map[string]string{"ENABLED": "true", "URL": cfg.URL, "ISSUER": cfg.Issuer, "AUDIENCE": cfg.Audience, "PEER_SECRET": cfg.PeerSecret, "TIMEOUT_MS": "1000"} {
		t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_"+key, value)
	}
	for _, value := range []string{"", "false", "TRUE", " 1 "} {
		t.Setenv(flag, value)
		c, err = NewPublicationDescriptionDerivationClientFromEnv()
		if err != nil || c.readEnabled != (strings.TrimSpace(value) == "1" || value == "TRUE") {
			t.Fatalf("read flag %q: %v", value, err)
		}
	}
}

func TestPublicationDescriptionCopyClientExactPrivateHTTPSContract(t *testing.T) {
	raw := []byte{'{', '"', 'a', '"', ':', '"', 0, 255, 128, '"', '}'}
	var app store.SkillHubPublicationDescriptionApplication
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.URL.Path != publicationDescriptionReadPath || r.Header.Get("Authorization") != "Bearer pubderive_"+strings.Repeat("a", 64) ||
			r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Accept") != "application/octet-stream" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("unexpected private request")
		}
		body, _ := io.ReadAll(r.Body)
		var request map[string]json.RawMessage
		if json.Unmarshal(body, &request) != nil || len(request) != 8 {
			t.Error("unexpected request schema")
		}
		want := map[string]any{"schemaVersion": 1, "applicationId": app.ID, "taskId": app.Input.Receipt.TaskID,
			"requestHash": app.Input.Receipt.Query.RequestHash, "candidateHash": app.Input.Draft.Evidence.CandidateHash,
			"proposal": app.Input.Draft.Proposal, "expectedReceipt": app.Result.Derivation, "derivedBytes": app.Result.RequestBytes}
		for key, value := range want {
			b, _ := json.Marshal(value)
			if !reflect.DeepEqual(request[key], json.RawMessage(b)) {
				t.Errorf("binding %s mismatch", key)
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		_, _ = w.Write(raw)
	}))
	defer s.Close()
	app = completedDescriptionApplication(t, s.URL, raw)
	cfg := derivationClientConfig(s)
	cfg.ReadEnabled = true
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(s.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "must-not-leak"}})
	cfg.HTTPClient.Jar = jar
	for i := 0; i < 2; i++ {
		// Recreate the client to model transport restart. No fallback derive call.
		c, err := NewPublicationDescriptionDerivationClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.ReadRetainedCopy(context.Background(), app)
		if err != nil || !reflect.DeepEqual(got, raw) {
			t.Fatalf("exact bytes: %v %v", got, err)
		}
	}
	if requests.Load() != 2 || cfg.HTTPClient.Jar != jar {
		t.Fatal("recovery changed caller client or retried derivation")
	}
}

func TestPublicationDescriptionCopyClientRejectsInputsBeforeDispatch(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer s.Close()
	cfg := derivationClientConfig(s)
	cfg.ReadEnabled = true
	c, _ := NewPublicationDescriptionDerivationClient(cfg)
	mutations := map[string]func(*store.SkillHubPublicationDescriptionApplication){
		"not completed":        func(a *store.SkillHubPublicationDescriptionApplication) { a.Result = nil },
		"missing applied time": func(a *store.SkillHubPublicationDescriptionApplication) { a.AppliedAt = nil },
		"zero created time":    func(a *store.SkillHubPublicationDescriptionApplication) { a.CreatedAt = time.Time{} },
		"identity":             func(a *store.SkillHubPublicationDescriptionApplication) { a.ID += "0" },
		"confirmation": func(a *store.SkillHubPublicationDescriptionApplication) {
			a.Input.ConfirmationKey = strings.Repeat("f", 64)
		},
		"proposal": func(a *store.SkillHubPublicationDescriptionApplication) {
			a.Result.Derivation.ProposalDigest = strings.Repeat("f", 64)
		},
		"size": func(a *store.SkillHubPublicationDescriptionApplication) {
			a.Result.RequestBytes = store.SkillHubPublicationCaptureMaxBytes + 1
		},
		"receipt": func(a *store.SkillHubPublicationDescriptionApplication) {
			a.Result.Derivation.OriginalRequestHash = strings.Repeat("f", 64)
		},
		"issuer": func(a *store.SkillHubPublicationDescriptionApplication) {
			a.Input.Receipt.Query.Issuer = "https://wrong.example"
			a.IdentityHash, _ = a.Input.IdentityHash()
			a.ID, a.Result.ApplicationID = "pubdesc_"+a.IdentityHash, "pubdesc_"+a.IdentityHash
		},
		"audience": func(a *store.SkillHubPublicationDescriptionApplication) {
			a.Input.Receipt.Query.Audience = "https://wrong.example"
			a.IdentityHash, _ = a.Input.IdentityHash()
			a.ID, a.Result.ApplicationID = "pubdesc_"+a.IdentityHash, "pubdesc_"+a.IdentityHash
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			app := completedDescriptionApplication(t, s.URL, []byte("private"))
			mutate(&app)
			if b, err := c.ReadRetainedCopy(context.Background(), app); b != nil || err != ErrSkillHubPublicationDescriptionDerivationInput {
				t.Fatalf("invalid read: %v %v", b, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if b, err := c.ReadRetainedCopy(ctx, completedDescriptionApplication(t, s.URL, []byte("private"))); b != nil || err != context.Canceled {
		t.Fatal("cancelled input dispatched", err)
	}
	if requests.Load() != 0 {
		t.Fatal("invalid inputs dispatched")
	}
}

func TestPublicationDescriptionCopyClientRejectsMalformedPeerBytes(t *testing.T) {
	raw := []byte("private-package-bytes")
	for _, name := range []string{"error", "wrong type", "duplicate type", "encoding", "missing length", "short length", "long length", "truncated", "changed hash", "redirect"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
				body := raw
				switch name {
				case "error":
					w.WriteHeader(409)
				case "wrong type":
					w.Header().Set("Content-Type", "application/json")
				case "duplicate type":
					w.Header().Add("Content-Type", "application/octet-stream")
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "missing length":
					w.Header().Del("Content-Length")
					w.(http.Flusher).Flush()
				case "short length":
					w.Header().Set("Content-Length", strconv.Itoa(len(raw)-1))
					body = body[:len(body)-1]
				case "long length":
					w.Header().Set("Content-Length", strconv.Itoa(len(raw)+1))
					body = append(append([]byte(nil), body...), '!')
				case "truncated":
					body = body[:len(body)-1]
				case "changed hash":
					body = []byte(strings.Repeat("X", len(raw)))
				case "redirect":
					w.Header().Set("Location", "/must-not-follow")
					w.WriteHeader(307)
				}
				_, _ = w.Write(body)
			}))
			defer s.Close()
			cfg := derivationClientConfig(s)
			cfg.ReadEnabled = true
			cfg.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("caller redirect policy invoked"); return nil }
			c, _ := NewPublicationDescriptionDerivationClient(cfg)
			got, err := c.ReadRetainedCopy(context.Background(), completedDescriptionApplication(t, s.URL, raw))
			if got != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionDerivationPeer) || requests.Load() != 1 {
				t.Fatalf("unsafe response accepted/retried: got=%v err=%v calls=%d", got, err, requests.Load())
			}
		})
	}
}

func TestPublicationDescriptionCopyClientCancellationAndTimeout(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, cancelCaller := range []bool{false, true} {
			t.Run(strconv.FormatBool(partial)+"/"+strconv.FormatBool(cancelCaller), func(t *testing.T) {
				started := make(chan struct{})
				stop := make(chan struct{})
				s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if partial {
						w.Header().Set("Content-Type", "application/octet-stream")
						w.Header().Set("Content-Length", "7")
						_, _ = w.Write([]byte("pri"))
						w.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-r.Context().Done():
					case <-stop:
					}
				}))
				defer s.Close()
				defer close(stop)
				cfg := derivationClientConfig(s)
				cfg.ReadEnabled, cfg.Timeout = true, time.Second
				c, _ := NewPublicationDescriptionDerivationClient(cfg)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cancelCaller {
					go func() { <-started; cancel() }()
				}
				got, err := c.ReadRetainedCopy(ctx, completedDescriptionApplication(t, s.URL, []byte("private")))
				if got != nil || err == nil || (cancelCaller && !errors.Is(err, context.Canceled)) {
					t.Fatalf("partial bytes escaped: %v %v", got, err)
				}
			})
		}
	}
}
