package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func TestPublicationDescriptionDerivationClientDefaultOff(t *testing.T) {
	c, err := NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	app := clientDescriptionApplication()
	if _, err := c.DeriveAndRetain(context.Background(), app); err != ErrSkillHubPublicationDescriptionDerivationDisabled {
		t.Fatalf("error = %v, want disabled", err)
	}
}

func TestPublicationDescriptionDerivationClientStrictPeerContract(t *testing.T) {
	app := clientDescriptionApplication()
	secret := "pubderive_" + strings.Repeat("a", 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != publicationDescriptionDerivationPath {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("authorization = %q", got)
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Error("browser credentials leaked to peer")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["applicationId"] != app.ID || body["taskId"] != app.Input.Receipt.TaskID || body["requestHash"] != app.Input.Receipt.Query.RequestHash || body["candidateHash"] != app.Input.Draft.Evidence.CandidateHash {
			t.Errorf("unexpected request bindings: %#v", body)
		}
		p := app.Input.Draft.Proposal
		entry := *p.EntryIndex
		receipt := store.SkillHubPublicationDescriptionDerivationReceipt{
			SchemaVersion: "1", Algorithm: "skillhub-description-derivation-v1",
			OriginalRequestHash:   app.Input.Receipt.Query.RequestHash,
			OriginalCandidateHash: app.Input.Draft.Evidence.CandidateHash,
			ValidationDigest:      strings.Repeat("5", 64), ProposalDigest: publicationDescriptionProposalDigest(p),
			LimitsFingerprint: strings.Repeat("7", 64), DerivedRequestHash: strings.Repeat("8", 64), DerivedCandidateHash: strings.Repeat("9", 64),
		}
		_ = entry
		_ = json.NewEncoder(w).Encode(map[string]any{
			"applicationId": app.ID, "taskId": app.Input.Receipt.TaskID,
			"derivedBytes":       101,
			"derivedRequestHash": receipt.DerivedRequestHash, "derivedCandidateHash": receipt.DerivedCandidateHash,
			"receipt": receipt, "replayed": false,
		})
	}))
	defer server.Close()
	parsed := server.URL + publicationDescriptionDerivationPath
	c, err := NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{
		Enabled: true, URL: parsed, Issuer: server.URL, Audience: server.URL, PeerSecret: secret,
		Timeout: 5 * time.Second, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.DeriveAndRetain(context.Background(), app)
	if err != nil {
		t.Fatal(err)
	}
	if result.ApplicationID != app.ID || result.Derivation.DerivedCandidateHash != strings.Repeat("9", 64) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestPublicationDescriptionDerivationClientRejectsUnknownResponseFields(t *testing.T) {
	app := clientDescriptionApplication()
	secret := "pubderive_" + strings.Repeat("b", 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"applicationId":"` + app.ID + `","taskId":"` + app.Input.Receipt.TaskID + `","derivedRequestHash":"` + strings.Repeat("8", 64) + `","derivedCandidateHash":"` + strings.Repeat("9", 64) + `","receipt":{"schemaVersion":"1","algorithm":"skillhub-description-derivation-v1","originalRequestHash":"` + app.Input.Receipt.Query.RequestHash + `","originalCandidateHash":"` + app.Input.Draft.Evidence.CandidateHash + `","validationDigest":"` + strings.Repeat("5", 64) + `","proposalDigest":"` + publicationDescriptionProposalDigest(app.Input.Draft.Proposal) + `","limitsFingerprint":"` + strings.Repeat("7", 64) + `","derivedRequestHash":"` + strings.Repeat("8", 64) + `","derivedCandidateHash":"` + strings.Repeat("9", 64) + `"},"unexpected":true}`))
	}))
	defer server.Close()
	c, err := NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{Enabled: true, URL: server.URL + publicationDescriptionDerivationPath, Issuer: server.URL, Audience: server.URL, PeerSecret: secret, Timeout: 5 * time.Second, HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeriveAndRetain(context.Background(), app); err != ErrSkillHubPublicationDescriptionDerivationPeer {
		t.Fatalf("error = %v, want peer rejection", err)
	}
}

func clientDescriptionApplication() store.SkillHubPublicationDescriptionApplication {
	index := int64(0)
	return store.SkillHubPublicationDescriptionApplication{
		ID: "pubdesc_" + strings.Repeat("a", 64),
		Input: store.SkillHubPublicationDescriptionApplicationInput{
			Receipt: store.SkillHubPublicationCloudReceipt{
				Query:        store.SkillHubPublicationUploadQuery{EntryID: "pubentry_" + strings.Repeat("b", 32), Issuer: "https://cats.example", Audience: "https://hub.example", Action: store.SkillHubPublicationUploadAction, Revision: 1, Epoch: 1, RequestHash: strings.Repeat("1", 64), SourceSnapshotHash: strings.Repeat("2", 64), RequestBytes: 100},
				DelegationID: "pubgrant_" + strings.Repeat("c", 32), IdempotencyKey: "pubcandidate_" + strings.Repeat("d", 32), TaskID: "pubtask_12345678-1234-4123-8123-123456789abc", EventID: "pubevent_12345678-1234-4123-8123-123456789abc",
			},
			Draft: store.SkillHubPublicationDescriptionDraftInput{Evidence: store.SkillHubPublicationRepairEvidence{CandidateHash: strings.Repeat("2", 64), ReportHash: strings.Repeat("3", 64), IssueSetHash: strings.Repeat("4", 64)}, Proposal: store.SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &index, Text: "Read a document."}},
		},
	}
}
