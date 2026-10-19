package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

const publicationCommitCloudPath = "/api/skills/publication-delegations/commit"

var errPublicationCommitCloud = errors.New("publication Cloud commit unavailable")
var errPublicationCommitCloudDisabled = errors.New("publication Cloud commit disabled")
var publicationPackageID = regexp.MustCompile(`^pkg_[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

type PublicationCommitCloudConfig struct {
	Enabled                      bool
	Issuer, Audience, PeerSecret string
}
type PublicationCommitCloudClient struct {
	config PublicationCommitCloudConfig
	client *http.Client
	active atomic.Int32
}
type PublicationCommitCloudResult struct {
	SkillID     string `json:"skillId"`
	Version     string `json:"version"`
	PackageID   string `json:"packageId"`
	Checksum    string `json:"checksumSha256"`
	PublishedAt string `json:"publishedAt"`
}

// Internal observation, NOT a user response or authorization credential.
type PublicationCommitCloudStatus struct {
	SchemaVersion int                                    `json:"schemaVersion"`
	Receipt       publicationCloudReceiptRequest         `json:"receipt"`
	State         string                                 `json:"state"`
	Target        *store.SkillHubPublicationCommitTarget `json:"target"`
	PermitID      *string                                `json:"permitId"`
	Result        *PublicationCommitCloudResult          `json:"result"`
	ErrorCode     *string                                `json:"errorCode"`
}

// Explicit construction only: no environment flag, browser route, Hub worker,
// human-intent write, retry loop or model call is registered by this client.
func NewPublicationCommitCloudClient(c PublicationCommitCloudConfig) (*PublicationCommitCloudClient, error) {
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, MaxConnsPerHost: 4,
		DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, TLSHandshakeTimeout: 2 * time.Second,
		ResponseHeaderTimeout: 21 * time.Second, MaxResponseHeaderBytes: 8192, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return newPublicationCommitCloudClient(c, transport)
}
func newPublicationCommitCloudClient(c PublicationCommitCloudConfig, transport http.RoundTripper) (*PublicationCommitCloudClient, error) {
	if c.Enabled && (!store.ValidSkillHubPublicationOrigin(c.Issuer) || !store.ValidSkillHubPublicationOrigin(c.Audience) || c.Issuer == c.Audience ||
		!regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.PeerSecret) || transport == nil) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	return &PublicationCommitCloudClient{config: c, client: &http.Client{Transport: transport, Timeout: 22 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *PublicationCommitCloudClient) send(ctx context.Context, r store.SkillHubPublicationCloudReceipt, action string, confirm bool) ([]byte, error) {
	if !c.config.Enabled {
		return nil, errPublicationCommitCloudDisabled
	}
	if r.Validate() != nil || r.Query.Issuer != c.config.Issuer || r.Query.Audience != c.config.Audience {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if c.active.Add(1) > 4 {
		c.active.Add(-1)
		return nil, store.ErrSkillHubPublicationBusy
	}
	defer c.active.Add(-1)
	ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	input := map[string]any{"schemaVersion": 1, "action": action, "receipt": publicationControlReceipt(r)}
	if action == "plan" {
		input["confirmVersionPublish"] = confirm
	}
	data, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Audience+publicationCommitCloudPath, bytes.NewReader(data))
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	req.Header.Set("Authorization", "Bearer "+c.config.PeerSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	res, err := c.client.Do(req)
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	defer res.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Cache-Control"} {
		if len(res.Header.Values(key)) > 1 {
			return nil, errPublicationCommitCloud
		}
	}
	media, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) || res.Header.Get("Content-Encoding") != "" || res.ContentLength > 8192 {
		return nil, errPublicationCommitCloud
	}
	noStore := false
	for _, v := range strings.Split(res.Header.Get("Cache-Control"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "no-store") {
			noStore = true
		}
	}
	if !noStore {
		return nil, errPublicationCommitCloud
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(data) == 0 || len(data) > 8192 || !utf8.Valid(data) {
		return nil, errPublicationCommitCloud
	}
	if res.StatusCode == 200 {
		return data, nil
	}
	object, err := publicationExactObject(data, "error")
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	e, err := publicationExactObject(object["error"], "code", "message")
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	var code, message string
	if json.Unmarshal(e["code"], &code) != nil || json.Unmarshal(e["message"], &message) != nil {
		return nil, errPublicationCommitCloud
	}
	switch {
	case res.StatusCode == 403 && code == "skillhub.delegated_commit_publisher_changed":
		return nil, store.ErrSkillHubPublicationDenied
	case res.StatusCode == 409 && code == "skillhub.delegated_commit_cancelled":
		return nil, store.ErrSkillHubPublicationSettled
	case res.StatusCode == 409 && code == "skillhub.delegated_commit_conflict":
		return nil, store.ErrSkillHubPublicationConflict
	}
	// Missing plans, rejection and confirmation-required are not success, approval
	// or automatic format-repair instructions. Higher adapters handle them later.
	return nil, errPublicationCommitCloud
}

// Plan reserves one immutable target. The bool is not human authorization.
func (c *PublicationCommitCloudClient) Plan(ctx context.Context, r store.SkillHubPublicationCloudReceipt, confirm bool) (*store.SkillHubPublicationCommitInput, error) {
	data, err := c.send(ctx, r, "plan", confirm)
	if err != nil {
		return nil, err
	}
	wire, err := decodePublicationCommitPermit(data)
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	in := wire.input(c.config.Issuer, c.config.Audience)
	if in.Receipt != r || in.Target.CandidateHash != r.Query.SourceSnapshotHash || in.Target.ConfirmVersionPublish != confirm {
		return nil, errPublicationCommitCloud
	}
	return &in, nil
}

// Authorize cannot record trusted human intent. Cloud must obtain the exact
// permit from CatsCo; a service credential/receipt alone cannot authorize it.
// An ACK says only commit accepted/current state, never final publication.
func (c *PublicationCommitCloudClient) Authorize(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
	data, err := c.send(ctx, r, "authorize", false)
	if err != nil {
		return nil, err
	}
	o, err := publicationExactObject(data, "schemaVersion", "receipt", "state", "permitId")
	if err != nil {
		return nil, errPublicationCommitCloud
	}
	wire, err := decodePublicationCloudReceipt(o["receipt"])
	if err != nil || wire != publicationControlReceipt(r) {
		return nil, errPublicationCommitCloud
	}
	var out PublicationCommitCloudStatus
	if json.Unmarshal(data, &out) != nil || out.SchemaVersion != 1 || out.PermitID == nil || !publicationPermitID.MatchString(*out.PermitID) || !publicationCloudCommitState(out.State) {
		return nil, errPublicationCommitCloud
	}
	return &out, nil
}
func publicationCloudCommitState(s string) bool {
	return s == "committing" || s == "publish_queued" || s == "publish_failed" || s == "published"
}

func decodePublicationCommitStatus(data []byte) (PublicationCommitCloudStatus, error) {
	var out PublicationCommitCloudStatus
	// Null is meaningful only for the four optional status fields. Token parsing
	// preserves duplicate-key detection rather than round-tripping through a map.
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return out, errPublicationCommitCloud
	}
	allowed := map[string]bool{"schemaVersion": false, "receipt": false, "state": false, "target": true, "permitId": true, "result": true, "errorCode": true}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		nullable, known := allowed[key]
		if err != nil || !ok || !known || fields[key] != nil {
			return out, errPublicationCommitCloud
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || (!nullable && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return out, errPublicationCommitCloud
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return out, errPublicationCommitCloud
	}
	if _, err = d.Token(); err != io.EOF || len(fields) != len(allowed) {
		return out, errPublicationCommitCloud
	}
	if json.Unmarshal(data, &out) != nil || out.SchemaVersion != 1 {
		return out, errPublicationCommitCloud
	}
	if _, err := decodePublicationCloudReceipt(fields["receipt"]); err != nil {
		return out, errPublicationCommitCloud
	}
	if out.Target != nil {
		if _, err := publicationExactObject(fields["target"], "candidateHash", "manifestHash", "publisherUserId", "publisherProfileId", "namespace", "skillId", "version", "uploadId", "releaseAt", "confirmVersionPublish"); err != nil || out.Target.Validate() != nil {
			return out, errPublicationCommitCloud
		}
	}
	if out.Result != nil {
		if _, err := publicationExactObject(fields["result"], "skillId", "version", "packageId", "checksumSha256", "publishedAt"); err != nil {
			return out, errPublicationCommitCloud
		}
	}
	return out, nil
}
func (c *PublicationCommitCloudClient) Read(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
	data, err := c.send(ctx, r, "read", false)
	if err != nil {
		return nil, err
	}
	out, err := decodePublicationCommitStatus(data)
	if err != nil || out.Receipt != publicationControlReceipt(r) {
		return nil, errPublicationCommitCloud
	}
	if out.Target != nil && out.Target.CandidateHash != r.Query.SourceSnapshotHash {
		return nil, errPublicationCommitCloud
	}
	if publicationCloudCommitState(out.State) {
		if out.Target == nil || out.PermitID == nil || !publicationPermitID.MatchString(*out.PermitID) {
			return nil, errPublicationCommitCloud
		}
	} else if (out.State != "delegated_pending" && out.State != "cancelled") || out.PermitID != nil {
		return nil, errPublicationCommitCloud
	}
	if out.State == "published" {
		p := out.Result
		t := out.Target
		if p == nil || p.SkillID != t.SkillID || p.Version != t.Version || p.PublishedAt != t.ReleaseAt || !publicationPackageID.MatchString(p.PackageID) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(p.Checksum) {
			return nil, errPublicationCommitCloud
		}
	} else if out.Result != nil {
		return nil, errPublicationCommitCloud
	}
	if out.ErrorCode != nil {
		allowed := false
		for _, suffix := range []string{"owner_inactive", "publisher_changed", "prepared_changed", "lease_lost", "publish_unavailable", "publish_attempts_exhausted", "final_validation_failed"} {
			if *out.ErrorCode == "skillhub.publication_task_"+suffix {
				allowed = true
			}
		}
		if !allowed {
			return nil, errPublicationCommitCloud
		}
	}
	return &out, nil
}
