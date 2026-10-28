package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Real Cloud PostgreSQL/permit/admission HTTPS and the actual CatsCo
// coordinator/client. CatsCo authority and Runtime source remain synthetic;
// this is not a production or user-facing E2E test.
func TestPublicationDescriptionReleaseCommitPairedCloud(t *testing.T) {
	root, node := os.Getenv("SKILLHUB_DESCRIPTION_COMMIT_ROOT"), os.Getenv("SKILLHUB_DESCRIPTION_COMMIT_NODE")
	if root == "" || node == "" || os.Getenv("SKILLHUB_TEST_DATABASE_URL") == "" {
		t.Skip("use the mandatory pinned PostgreSQL paired runner")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join("..", "scripts", "fixtures", "publication-description-plan-cloud.mjs"), root)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			t.Errorf("Cloud fixture failed: %v %s", err, stderr.String())
		}
	})
	decoder := json.NewDecoder(io.LimitReader(out, 256*1024))
	var ready struct {
		Origin             string                         `json:"origin"`
		Certificate        string                         `json:"certificate"`
		PublicationReceipt publicationCloudReceiptRequest `json:"publicationReceipt"`
		OriginalValidation PublicationValidationReport    `json:"originalValidation"`
		DerivedRawJson     string                         `json:"derivedRawJson"`
		Request            struct {
			Proposal        store.SkillHubPublicationDescriptionProposal          `json:"proposal"`
			ExpectedReceipt store.SkillHubPublicationDescriptionDerivationReceipt `json:"expectedReceipt"`
			DerivedBytes    int64                                                 `json:"derivedBytes"`
		} `json:"request"`
	}
	if err := decoder.Decode(&ready); err != nil {
		t.Fatal("Cloud readiness", err)
	}
	db, handoff := descriptionPlanHandoff(t)
	app := snapshotPublicationDescriptionApplication(handoff.checkpoint.Application)
	app.Input.Receipt = (publicationCommitPermitRequest{Receipt: ready.PublicationReceipt}).input("https://cats.example", ready.Origin).Receipt
	app.Input.Draft.Proposal = ready.Request.Proposal
	app.Input.Draft.Evidence, err = publicationRepairEvidence(ready.OriginalValidation, ready.PublicationReceipt.SourceSnapshotHash)
	if err != nil {
		t.Fatal(err)
	}
	task := db.task
	task.Input.Receipt, task.Input.Evidence = app.Input.Receipt, app.Input.Draft.Evidence
	task.IdentityHash, err = task.Input.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	round := &task.Rounds[0]
	round.Lease.EntryID = app.Input.Receipt.Query.EntryID
	round.Lease.Input.Evidence = app.Input.Draft.Evidence
	round.Result.Evidence = app.Input.Draft.Evidence
	app.Input.Draft.TaskIdentityHash = task.IdentityHash
	app.Input.Draft.RoundIdentityHash, err = round.Lease.Input.IdentityHash()
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
	app.Result = &store.SkillHubPublicationDescriptionApplicationResult{ApplicationID: app.ID, RequestBytes: ready.Request.DerivedBytes, Derivation: ready.Request.ExpectedReceipt}
	draft := db.draft
	draft.EntryID, draft.Input, draft.ConfirmationKey = app.Input.Receipt.Query.EntryID, app.Input.Draft, app.Input.ConfirmationKey
	accepted := *db.accepted
	accepted.ApplicationID, accepted.Result, accepted.Report = app.ID, *app.Result, descriptionDerivedReport(app)
	accepted.IdentityHash, err = accepted.Digest()
	if err != nil {
		t.Fatal(err)
	}
	db.app, db.task, db.draft, db.accepted = &app, task, draft, &accepted
	handoff = &publicationDescriptionContinuation{checkpoint: store.SkillHubPublicationDescriptionContinuation{Task: task, Draft: draft, Application: app, Acceptance: accepted}, bytes: []byte(ready.DerivedRawJson)}
	if handoff.checkpoint.Validate() != nil || validatePublicationDescriptionCopyApplication(app) != nil {
		t.Fatal("invalid CatsCo checkpoint")
	}
	command := func(value any) map[string]any {
		t.Helper()
		if err := json.NewEncoder(in).Encode(value); err != nil {
			t.Fatal(err)
		}
		var ack map[string]any
		if err := decoder.Decode(&ack); err != nil || ack["ok"] != true {
			t.Fatal("Cloud control", err, ack)
		}
		return ack
	}
	command(map[string]any{"action": "bind", "applicationId": app.ID})
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ready.Certificate)) {
		t.Fatal("missing test CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client, err := NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{
		Enabled: true, PlanEnabled: true, ReleaseAuthorizeEnabled: true, ReleaseCommitEnabled: true,
		URL: ready.Origin + publicationDescriptionDerivationPath, Issuer: "https://cats.example", Audience: ready.Origin,
		PeerSecret: "pubderive_" + strings.Repeat("a", 64), Timeout: 10 * time.Second,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	flowDB := &publicationDescriptionReleaseFlowStore{descriptionContinuationTestStore: db}
	planned, err := planConfirmedPublicationDescription(ctx, flowDB, 1, app.Input.Receipt.Query.EntryID, handoff, false,
		publicationDescriptionReleasePlanPorts{VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { return nil }, PlanRetainedCopy: client.PlanRetainedCopy})
	if err != nil || planned == nil {
		t.Fatal("real Cloud plan failed", err)
	}
	flowDB.intent = releaseFlowIntent(planned.checkpoint, planned.plan.Target)
	command(map[string]any{"action": "set-intent-hash", "intentHash": flowDB.intent.IdentityHash})
	ports := publicationDescriptionReleasePorts{publicationDescriptionContinuationPorts: publicationDescriptionContinuationPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { return nil },
	},
		PlanRetainedCopy: client.PlanRetainedCopy,
		AuthorizeCloud: func(ctx context.Context, app store.SkillHubPublicationDescriptionApplication, intent store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error) {
			return client.AuthorizeRetainedRelease(ctx, app, intent)
		},
		AdmitCloud: func(ctx context.Context, app store.SkillHubPublicationDescriptionApplication, intent store.SkillHubPublicationDescriptionReleaseIntent, permit publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseAdmission, error) {
			return client.AdmitRetainedRelease(ctx, app, intent, permit)
		},
	}
	got, err := admitConfirmedPublicationDescriptionReleaseFlow(ctx, flowDB, 1, app.Input.Receipt.Query.EntryID, ports)
	if err != nil || got == nil || got.admission.State != "queued" || got.admission.Replayed == nil || *got.admission.Replayed {
		t.Fatal("real Cloud admission failed", err, got)
	}
	stats := command(map[string]any{"action": "stats"})
	if stats["derivedCommits"] != float64(1) || stats["commits"] != float64(0) || stats["jobs"] != float64(0) {
		t.Fatal("admission touched the wrong publication path", stats)
	}
	command(map[string]any{"action": "lose-next"})
	if replay, err := client.AdmitRetainedRelease(ctx, app, *flowDB.intent, got.authorized.permit); replay != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionReleaseCommitPeer) {
		t.Fatal("lost admission response was not reported as ambiguous", replay, err)
	}
	replay, err := client.AdmitRetainedRelease(ctx, app, *flowDB.intent, got.authorized.permit)
	if err != nil || replay == nil || replay.State != "queued" || replay.Replayed == nil || !*replay.Replayed {
		t.Fatal("admission replay did not recover the durable row", err, replay)
	}
}
