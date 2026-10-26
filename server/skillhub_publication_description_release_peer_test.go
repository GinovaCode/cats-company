package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type publicationDescriptionPermitPeerStore struct {
	store.Store
	store.SkillHubPublicationDescriptionReleasePermitStore
	calls atomic.Int32
	fn    func(context.Context, string, string, store.SkillHubPublicationCommitTarget) (*store.SkillHubPublicationDescriptionReleasePermit, error)
}

func (s *publicationDescriptionPermitPeerStore) AuthorizeSkillHubPublicationDescriptionRelease(ctx context.Context, entryID, applicationID string, target store.SkillHubPublicationCommitTarget) (*store.SkillHubPublicationDescriptionReleasePermit, error) {
	s.calls.Add(1)
	return s.fn(ctx, entryID, applicationID, target)
}

type publicationDescriptionPermitRequest struct {
	SchemaVersion int                                   `json:"schemaVersion"`
	EntryID       string                                `json:"entryId"`
	ApplicationID string                                `json:"applicationId"`
	Target        store.SkillHubPublicationCommitTarget `json:"target"`
}

func descriptionPermitFixture(t *testing.T) (*SkillHubPublicationPeerHandler, *publicationDescriptionPermitPeerStore, publicationDescriptionPermitRequest) {
	t.Helper()
	peerFixture(t)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_INTROSPECTION_ENABLED", "")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_RECEIPTS_ENABLED", "")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_COMMIT_PERMITS_ENABLED", "")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_PERMITS_ENABLED", "true")
	target := store.SkillHubPublicationCommitTarget{CandidateHash: strings.Repeat("a", 64), ManifestHash: strings.Repeat("b", 64),
		PublisherUserID: "usr_test", PublisherProfileID: "pub_test", Namespace: "publisher", SkillID: "publisher/example",
		Version: "1.0.0", UploadID: "upl_test", ReleaseAt: "2026-10-26T00:00:00.000Z", ConfirmVersionPublish: true}
	appID := "pubdesc_" + strings.Repeat("c", 64)
	b := publicationDescriptionPermitRequest{SchemaVersion: 1, EntryID: publicationReadTestID, ApplicationID: appID, Target: target}
	s := &publicationDescriptionPermitPeerStore{}
	s.fn = func(ctx context.Context, entryID, applicationID string, got store.SkillHubPublicationCommitTarget) (*store.SkillHubPublicationDescriptionReleasePermit, error) {
		if _, ok := ctx.Deadline(); !ok || entryID != b.EntryID || applicationID != b.ApplicationID || got != b.Target {
			t.Error("authorization did not receive the exact request in a bounded context")
		}
		intent := store.SkillHubPublicationDescriptionReleaseIntent{EntryID: entryID, ActorUID: 7, ApplicationID: applicationID,
			AcceptanceIdentityHash: strings.Repeat("d", 64), Target: got, RequestedAt: time.Date(2026, 10, 26, 0, 0, 1, 0, time.UTC)}
		intent.IdentityHash, _ = intent.Digest()
		permit := &store.SkillHubPublicationDescriptionReleasePermit{Intent: intent, ID: "pubdescpermit_" + strings.Repeat("e", 32), AuthorizedAt: intent.RequestedAt.Add(time.Second)}
		if err := permit.Validate(); err != nil {
			hash, digestErr := intent.Digest()
			t.Errorf("fixture permit invalid: %v digest=%q digestErr=%v idlen=%d prefixlen=%d time=%v", err, hash, digestErr, len(permit.ID), len("pubdescpermit_"), permit.AuthorizedAt.Before(intent.RequestedAt))
		}
		return permit, nil
	}
	return NewSkillHubPublicationPeerHandlerFromEnv(s), s, b
}

func descriptionPermitRequestHTTP(t *testing.T, b publicationDescriptionPermitRequest) *http.Request {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://cats.example"+SkillHubPublicationDescriptionReleasePermitPath, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+peerSecret)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestSkillHubPublicationDescriptionReleasePermitPeerProjection(t *testing.T) {
	h, s, b := descriptionPermitFixture(t)
	w := &peerRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.Handle(w, descriptionPermitRequestHTTP(t, b))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, s.calls.Load(), w.Body.String())
	}
	var got struct {
		SchemaVersion int                                   `json:"schemaVersion"`
		EntryID       string                                `json:"entryId"`
		ApplicationID string                                `json:"applicationId"`
		Target        store.SkillHubPublicationCommitTarget `json:"target"`
		IntentHash    string                                `json:"intentHash"`
		PermitID      string                                `json:"permitId"`
		AuthorizedAt  string                                `json:"authorizedAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.EntryID != b.EntryID || got.ApplicationID != b.ApplicationID || got.Target != b.Target ||
		got.PermitID != "pubdescpermit_"+strings.Repeat("e", 32) || got.AuthorizedAt != "2026-10-26T00:00:02Z" || s.calls.Load() != 1 {
		t.Fatalf("unexpected projection: %+v", got)
	}
	if len(got.IntentHash) != sha256.Size*2 {
		t.Fatal("missing intent digest")
	}
	if _, err := hex.DecodeString(got.IntentHash); err != nil {
		t.Fatal("invalid intent digest")
	}
}

func TestSkillHubPublicationDescriptionReleasePermitPeerStrictBody(t *testing.T) {
	for _, mode := range []string{"extra", "duplicate", "schema", "bad_application_id", "target_extra", "target_duplicate", "target_null", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			h, s, b := descriptionPermitFixture(t)
			r := descriptionPermitRequestHTTP(t, b)
			encoded, _ := json.Marshal(b)
			raw := string(encoded)
			switch mode {
			case "extra":
				raw = strings.TrimSuffix(raw, "}") + `,"actorUid":7}`
			case "duplicate":
				raw = strings.TrimSuffix(raw, "}") + `,"schemaVersion":1}`
			case "schema":
				raw = strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":2`, 1)
			case "bad_application_id":
				raw = strings.Replace(raw, b.ApplicationID, "invalid", 1)
			case "target_extra":
				raw = strings.Replace(raw, `"target":{`, `"target":{"approvedBy":7,`, 1)
			case "target_duplicate":
				raw = strings.Replace(raw, `"version":"1.0.0"`, `"version":"1.0.0","version":"1.0.0"`, 1)
			case "target_null":
				raw = strings.Replace(raw, `"target":{`, `"target":null,"ignored":{`, 1)
			case "trailing":
				raw += `{}`
			}
			r.Body = io.NopCloser(strings.NewReader(raw))
			r.ContentLength = int64(len(raw))
			runPeer(t, h, r, http.StatusBadRequest)
			if s.calls.Load() != 0 {
				t.Fatal("invalid request reached permit authorization")
			}
		})
	}
}

func TestSkillHubPublicationDescriptionReleasePermitPeerGating(t *testing.T) {
	_, s, b := descriptionPermitFixture(t)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_PERMITS_ENABLED", "")
	runPeer(t, NewSkillHubPublicationPeerHandlerFromEnv(s), descriptionPermitRequestHTTP(t, b), http.StatusNotFound)
	if s.calls.Load() != 0 {
		t.Fatal("disabled endpoint reached storage")
	}
}

func TestSkillHubPublicationDescriptionReleasePermitPeerErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{{"denied", store.ErrSkillHubPublicationDenied, http.StatusForbidden}, {"settled", store.ErrSkillHubPublicationSettled, http.StatusConflict}, {"conflict", store.ErrSkillHubPublicationConflict, http.StatusConflict}, {"storage", errors.New("db secret"), http.StatusServiceUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, b := descriptionPermitFixture(t)
			s.fn = func(context.Context, string, string, store.SkillHubPublicationCommitTarget) (*store.SkillHubPublicationDescriptionReleasePermit, error) {
				return nil, tc.err
			}
			runPeer(t, h, descriptionPermitRequestHTTP(t, b), tc.want)
		})
	}
}
