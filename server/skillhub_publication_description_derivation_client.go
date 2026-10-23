package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const publicationDescriptionDerivationPath = "/api/skills/publication-descriptions/internal/derive"

var (
	ErrSkillHubPublicationDescriptionDerivationDisabled = errors.New("skillhub publication description derivation is disabled")
	ErrSkillHubPublicationDescriptionDerivationConfig   = errors.New("invalid skillhub publication description derivation configuration")
	ErrSkillHubPublicationDescriptionDerivationPeer     = errors.New("skillhub publication description derivation peer rejected the request")
)

// PublicationDescriptionDerivationClientConfig is deliberately separate from
// the HTTP server configuration. A client is created, but never registered as
// a publication executor, unless a future rollout explicitly wires it in.
type PublicationDescriptionDerivationClientConfig struct {
	Enabled    bool
	URL        string
	Issuer     string
	Audience   string
	PeerSecret string
	Timeout    time.Duration
	HTTPClient *http.Client
}

type PublicationDescriptionDerivationClient struct {
	enabled  bool
	endpoint *url.URL
	secret   string
	client   *http.Client
}

func NewPublicationDescriptionDerivationClient(cfg PublicationDescriptionDerivationClientConfig) (*PublicationDescriptionDerivationClient, error) {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	if !cfg.Enabled {
		return &PublicationDescriptionDerivationClient{client: client}, nil
	}
	if cfg.Timeout < time.Second || cfg.Timeout > time.Minute || !strings.HasPrefix(cfg.PeerSecret, "pubderive_") || len(cfg.PeerSecret) != len("pubderive_")+64 {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	if !isLowerHex(cfg.PeerSecret[len("pubderive_"):]) || cfg.Issuer == "" || cfg.Audience == "" {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	endpoint, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != publicationDescriptionDerivationPath {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	issuer, err := url.Parse(strings.TrimSpace(cfg.Issuer))
	if err != nil || issuer.Scheme != "https" || issuer.User != nil || issuer.Host == "" || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	audience, err := url.Parse(strings.TrimSpace(cfg.Audience))
	if err != nil || audience.Scheme != "https" || audience.User != nil || audience.Host == "" || audience.RawQuery != "" || audience.Fragment != "" || endpoint.Host != audience.Host {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	client.Timeout = cfg.Timeout
	return &PublicationDescriptionDerivationClient{enabled: true, endpoint: endpoint, secret: cfg.PeerSecret, client: client}, nil
}

func NewPublicationDescriptionDerivationClientFromEnv() (*PublicationDescriptionDerivationClient, error) {
	enabled := strings.EqualFold(strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED")), "true") || os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED") == "1"
	timeout := 15 * time.Second
	if raw := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_TIMEOUT_MS")); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil {
			return nil, ErrSkillHubPublicationDescriptionDerivationConfig
		}
		timeout = time.Duration(ms) * time.Millisecond
	}
	return NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{
		Enabled:    enabled,
		URL:        strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_URL")),
		Issuer:     strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ISSUER")),
		Audience:   strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_AUDIENCE")),
		PeerSecret: strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_PEER_SECRET")),
		Timeout:    timeout,
	})
}

func (c *PublicationDescriptionDerivationClient) DeriveAndRetain(ctx context.Context, app store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
	if c == nil || !c.enabled {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationDisabled
	}
	if err := ctx.Err(); err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	proposal := app.Input.Draft.Proposal
	if app.ID == "" || app.Input.Receipt.Validate() != nil || app.Input.Draft.Evidence.Validate() != nil || proposal.Validate() != nil || proposal.RelativePath != "SKILL.md" || proposal.EntryIndex == nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	request := struct {
		SchemaVersion int    `json:"schemaVersion"`
		ApplicationID string `json:"applicationId"`
		TaskID        string `json:"taskId"`
		RequestHash   string `json:"requestHash"`
		CandidateHash string `json:"candidateHash"`
		Proposal      struct {
			RelativePath string `json:"relativePath"`
			Field        string `json:"field"`
			EntryIndex   int64  `json:"entryIndex"`
			Text         string `json:"text"`
		} `json:"proposal"`
	}{SchemaVersion: 1, ApplicationID: app.ID, TaskID: app.Input.Receipt.TaskID,
		RequestHash: app.Input.Receipt.Query.RequestHash, CandidateHash: app.Input.Draft.Evidence.CandidateHash}
	request.Proposal = struct {
		RelativePath string `json:"relativePath"`
		Field        string `json:"field"`
		EntryIndex   int64  `json:"entryIndex"`
		Text         string `json:"text"`
	}{proposal.RelativePath, proposal.Field, *proposal.EntryIndex, proposal.Text}
	body, err := json.Marshal(request)
	if err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if resp.StatusCode != http.StatusOK {
		return store.SkillHubPublicationDescriptionApplicationResult{}, fmt.Errorf("%w: status %d", ErrSkillHubPublicationDescriptionDerivationPeer, resp.StatusCode)
	}
	var out cloudPublicationDescriptionDerivationResponse
	if err := decodeUniqueJSON(raw, &out); err != nil || out.ApplicationID != app.ID || out.TaskID != app.Input.Receipt.TaskID || out.DerivedBytes < 1 || out.DerivedBytes > store.SkillHubPublicationCaptureMaxBytes || out.Receipt.OriginalRequestHash != app.Input.Receipt.Query.RequestHash || out.Receipt.OriginalCandidateHash != app.Input.Draft.Evidence.CandidateHash || out.Receipt.DerivedRequestHash == out.Receipt.OriginalRequestHash || out.Receipt.DerivedCandidateHash == out.Receipt.OriginalCandidateHash || out.Receipt.ProposalDigest != publicationDescriptionProposalDigest(proposal) {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	result := store.SkillHubPublicationDescriptionApplicationResult{ApplicationID: out.ApplicationID, RequestBytes: out.DerivedBytes, Derivation: out.Receipt}
	if err := result.ValidateFor(app); err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	return result, nil
}

type cloudPublicationDescriptionDerivationResponse struct {
	ApplicationID        string                                                `json:"applicationId"`
	TaskID               string                                                `json:"taskId"`
	DerivedBytes         int64                                                 `json:"derivedBytes"`
	DerivedRequestHash   string                                                `json:"derivedRequestHash"`
	DerivedCandidateHash string                                                `json:"derivedCandidateHash"`
	Receipt              store.SkillHubPublicationDescriptionDerivationReceipt `json:"receipt"`
	Replayed             bool                                                  `json:"replayed"`
}

func publicationDescriptionProposalDigest(p store.SkillHubPublicationDescriptionProposal) string {
	return publicationDescriptionSHA256Hex("skillhub-description-derivation-v1:proposal\x00{" +
		`"entryIndex":` + strconv.FormatInt(*p.EntryIndex, 10) + `,"field":` + jsonString(p.Field) + `,"relativePath":` + jsonString(p.RelativePath) + `,"text":` + jsonString(p.Text) + "}")
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }
func publicationDescriptionSHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
func isLowerHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// decodeUniqueJSON rejects duplicate object keys as well as unknown fields.
func decodeUniqueJSON(raw []byte, target any) error {
	var seen any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := decodeJSONValue(dec, &seen, 0); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("trailing json")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return err
	}
	if err := strict.Decode(&trailing); err != io.EOF {
		return err
	}
	return nil
}

func decodeJSONValue(dec *json.Decoder, out *any, depth int) error {
	if depth > 16 {
		return errors.New("json depth")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("json key")
				}
				if _, exists := obj[key]; exists {
					return errors.New("duplicate json key")
				}
				var value any
				if err := decodeJSONValue(dec, &value, depth+1); err != nil {
					return err
				}
				obj[key] = value
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			*out = obj
		case '[':
			arr := []any{}
			for dec.More() {
				var value any
				if err := decodeJSONValue(dec, &value, depth+1); err != nil {
					return err
				}
				arr = append(arr, value)
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			*out = arr
		default:
			return errors.New("json delimiter")
		}
	default:
		*out = v
	}
	return nil
}
