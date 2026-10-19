package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func commitClientTarget(r store.SkillHubPublicationCloudReceipt) store.SkillHubPublicationCommitTarget {
	return store.SkillHubPublicationCommitTarget{CandidateHash: r.Query.SourceSnapshotHash, ManifestHash: strings.Repeat("b", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test", Namespace: "publisher", SkillID: "publisher/test", Version: "1.0.0", UploadID: "upl_test", ReleaseAt: "2026-10-19T00:00:00.000Z", ConfirmVersionPublish: false}
}
func commitClientReply(action string, r store.SkillHubPublicationCloudReceipt) map[string]any {
	target := commitClientTarget(r)
	switch action {
	case "plan":
		return map[string]any{"schemaVersion": 1, "receipt": publicationControlReceipt(r), "target": target}
	case "authorize":
		return map[string]any{"schemaVersion": 1, "receipt": publicationControlReceipt(r), "state": "committing", "permitId": "pubpermit_" + strings.Repeat("c", 32)}
	default:
		return map[string]any{"schemaVersion": 1, "receipt": publicationControlReceipt(r), "state": "published", "target": target,
			"permitId": "pubpermit_" + strings.Repeat("c", 32), "errorCode": nil, "result": PublicationCommitCloudResult{SkillID: target.SkillID, Version: target.Version,
				PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("d", 64), PublishedAt: target.ReleaseAt}}
	}
}
func callCommitClient(c *PublicationCommitCloudClient, action string, r store.SkillHubPublicationCloudReceipt) error {
	ctx := context.Background()
	switch action {
	case "plan":
		_, err := c.Plan(ctx, r, false)
		return err
	case "authorize":
		_, err := c.Authorize(ctx, r)
		return err
	default:
		_, err := c.Read(ctx, r)
		return err
	}
}
func TestSkillHubPublicationCommitClientStrictTransport(t *testing.T) {
	for _, action := range []string{"plan", "authorize", "read"} {
		for _, mode := range []string{"success", "wrong_receipt", "duplicate", "nested_duplicate", "extra", "null", "wrong_type", "redirect", "encoding", "no_cache", "oversize", "wrong_schema", "unknown_state", "lost_reply"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				expected := cancellationTestLease("https://hub.example").Receipt
				peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "POST" || r.URL.Path != publicationCommitCloudPath || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("Cookie") != "" {
						t.Error("request transport scope")
					}
					var payload map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&payload) != nil {
						t.Error("request JSON")
					}
					var gotAction string
					json.Unmarshal(payload["action"], &gotAction)
					if gotAction != action {
						t.Error("wrong action")
					}
					if (action == "plan" && len(payload) != 4) || (action != "plan" && len(payload) != 3) {
						t.Error("extra request authority")
					}
					got, err := decodePublicationCloudReceipt(payload["receipt"])
					if err != nil || got != publicationControlReceipt(expected) {
						t.Error("receipt mismatch")
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Cache-Control", "no-store")
					reply := commitClientReply(action, expected)
					switch mode {
					case "wrong_receipt":
						changed := expected
						changed.TaskID = "pubtask_00000000-0000-4000-8000-000000000000"
						reply["receipt"] = publicationControlReceipt(changed)
					case "extra":
						reply["token"] = "private"
					case "null":
						reply["schemaVersion"] = nil
					case "wrong_type":
						reply["schemaVersion"] = "1"
					case "wrong_schema":
						reply["schemaVersion"] = 2
					case "unknown_state":
						if action == "plan" {
							target := commitClientTarget(expected)
							target.ConfirmVersionPublish = true
							reply["target"] = target
						} else {
							reply["state"] = "ready"
						}
					case "redirect":
						w.Header().Set("Location", "https://other.invalid/private")
						w.WriteHeader(302)
						return
					case "encoding":
						w.Header().Set("Content-Encoding", "gzip")
					case "no_cache":
						w.Header().Del("Cache-Control")
					case "oversize":
						w.Write([]byte(strings.Repeat("x", 8193)))
						return
					case "lost_reply":
						c, _, _ := w.(http.Hijacker).Hijack()
						c.Close()
						return
					}
					b, _ := json.Marshal(reply)
					text := string(b)
					if mode == "duplicate" {
						text = strings.Replace(text, "\"schemaVersion\":1", "\"schemaVersion\":1,\"schemaVersion\":1", 1)
					}
					if mode == "nested_duplicate" {
						text = strings.Replace(text, "\"candidateRevision\":1", "\"candidateRevision\":1,\"candidateRevision\":1", 1)
					}
					w.Write([]byte(text))
				}))
				defer peer.Close()
				expected.Query.Audience = peer.URL
				cfg := PublicationCommitCloudConfig{Enabled: true, Issuer: expected.Query.Issuer, Audience: peer.URL, PeerSecret: strings.Repeat("a", 64)}
				client, err := newPublicationCommitCloudClient(cfg, peer.Client().Transport)
				if err != nil {
					t.Fatal(err)
				}
				err = callCommitClient(client, action, expected)
				if (mode == "success") != (err == nil) {
					t.Fatalf("mode=%s err=%v", mode, err)
				}
				if err != nil && strings.Contains(err.Error(), "private") {
					t.Fatal("leaked upstream data")
				}
			})
		}
	}
}
func TestSkillHubPublicationCommitClientStatusConsistency(t *testing.T) {
	for _, mode := range []string{"pending", "cancelled", "committing", "queued", "failed", "no_permit", "bad_result", "result_early", "error_private", "missing_target", "null_candidate", "wrong_hash", "extra_result"} {
		t.Run(mode, func(t *testing.T) {
			expected := cancellationTestLease("https://hub.example").Receipt
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reply := commitClientReply("read", expected)
				switch mode {
				case "pending":
					reply["state"] = "delegated_pending"
					reply["target"] = nil
					reply["permitId"] = nil
					reply["result"] = nil
				case "cancelled":
					reply["state"] = "cancelled"
					reply["permitId"] = nil
					reply["result"] = nil
				case "committing":
					reply["state"] = "committing"
					reply["result"] = nil
				case "queued":
					reply["state"] = "publish_queued"
					reply["result"] = nil
					reply["errorCode"] = "skillhub.publication_task_publish_unavailable"
				case "failed":
					reply["state"] = "publish_failed"
					reply["result"] = nil
					reply["errorCode"] = "skillhub.publication_task_owner_inactive"
				case "no_permit":
					reply["permitId"] = nil
				case "missing_target":
					reply["target"] = nil
				case "result_early":
					reply["state"] = "committing"
				case "error_private":
					reply["errorCode"] = "raw SQL private secret"
				case "bad_result":
					p := reply["result"].(PublicationCommitCloudResult)
					p.Version = "2.0.0"
					reply["result"] = p
				case "wrong_hash":
					target := commitClientTarget(expected)
					target.CandidateHash = strings.Repeat("0", 64)
					reply["target"] = target
				case "extra_result":
					reply["result"] = map[string]any{"skillId": "publisher/test", "version": "1.0.0", "packageId": "pkg_x", "checksumSha256": strings.Repeat("d", 64), "publishedAt": "2026-10-19T00:00:00.000Z", "private": "source"}
				}
				data, _ := json.Marshal(reply)
				if mode == "null_candidate" {
					data = []byte(strings.Replace(string(data), "\"candidateHash\":\""+expected.Query.SourceSnapshotHash+"\"", "\"candidateHash\":null", 1))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.Write(data)
			}))
			defer peer.Close()
			expected.Query.Audience = peer.URL
			c, _ := newPublicationCommitCloudClient(PublicationCommitCloudConfig{Enabled: true, Issuer: expected.Query.Issuer, Audience: peer.URL, PeerSecret: strings.Repeat("a", 64)}, peer.Client().Transport)
			_, err := c.Read(context.Background(), expected)
			valid := mode == "pending" || mode == "cancelled" || mode == "committing" || mode == "queued" || mode == "failed"
			if valid != (err == nil) {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
		})
	}
}
func TestSkillHubPublicationCommitClientDefaultsScopeAndTLS(t *testing.T) {
	c, err := NewPublicationCommitCloudClient(PublicationCommitCloudConfig{})
	if err != nil {
		t.Fatal(err)
	}
	r := cancellationTestLease("https://hub.example").Receipt
	if !errors.Is(callCommitClient(c, "plan", r), errPublicationCommitCloudDisabled) {
		t.Fatal("default enabled")
	}
	for _, cfg := range []PublicationCommitCloudConfig{{Enabled: true}, {Enabled: true, Issuer: "http://bad", Audience: "https://hub.example", PeerSecret: strings.Repeat("a", 64)}} {
		if _, err := NewPublicationCommitCloudClient(cfg); err == nil {
			t.Fatal("accepted bad config")
		}
	}
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS or wrong scope reached handler") }))
	defer peer.Close()
	c, _ = NewPublicationCommitCloudClient(PublicationCommitCloudConfig{Enabled: true, Issuer: r.Query.Issuer, Audience: peer.URL, PeerSecret: strings.Repeat("a", 64)})
	if !errors.Is(callCommitClient(c, "plan", r), store.ErrSkillHubPublicationInvalid) {
		t.Fatal("scope not checked")
	}
	r.Query.Audience = peer.URL
	if !errors.Is(callCommitClient(c, "plan", r), errPublicationCommitCloud) {
		t.Fatal("untrusted cert accepted")
	}
}
func TestSkillHubPublicationCommitClientBoundedConcurrencyAndContext(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release; w.WriteHeader(503) }))
	defer peer.Close()
	defer once.Do(func() { close(release) })
	r := cancellationTestLease(peer.URL).Receipt
	c, _ := newPublicationCommitCloudClient(PublicationCommitCloudConfig{Enabled: true, Issuer: r.Query.Issuer, Audience: peer.URL, PeerSecret: strings.Repeat("a", 64)}, peer.Client().Transport)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { _, err := c.Read(ctx, r); done <- err }()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("did not enter")
		}
	}
	if _, err := c.Read(context.Background(), r); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal("concurrency unbounded")
	}
	cancel()
	for i := 0; i < 4; i++ {
		if err := <-done; !errors.Is(err, errPublicationCommitCloud) {
			t.Fatalf("context: %v", err)
		}
	}
	once.Do(func() { close(release) })
	if c.active.Load() != 0 {
		t.Fatal("slot leaked")
	}
}
