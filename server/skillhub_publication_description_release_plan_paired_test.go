package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
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

// Real Cloud PostgreSQL/intake/retention/worker/planner/HTTPS and actual CatsCo
// client/coordinator. CatsCo authority/acceptance and Runtime source are fake;
// this is not a production, model, permit, or dual-PostgreSQL E2E test.
func TestPublicationDescriptionReleasePlanPairedCloud(t *testing.T) {
	root, node := os.Getenv("SKILLHUB_DESCRIPTION_PLAN_ROOT"), os.Getenv("SKILLHUB_DESCRIPTION_PLAN_NODE")
	if root == "" || node == "" || os.Getenv("SKILLHUB_TEST_DATABASE_URL") == "" {
		t.Skip("use the mandatory pinned PostgreSQL paired runner")
	}
	for _, mode := range []string{"normal", "lose-response", "cancel", "publisher", "disable"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
				cancel()
				t.Fatal(err)
			}
			t.Cleanup(func() {
				defer cancel()
				_ = in.Close()
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("Cloud fixture failed: %v %s", err, stderr.String())
					}
				case <-time.After(5 * time.Second):
					cancel()
					<-done
					t.Errorf("Cloud fixture did not settle: %s", stderr.String())
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
					t.Fatal("Cloud control", err)
				}
				return ack
			}
			command(map[string]any{"action": "bind", "applicationId": app.ID})
			if mode == "lose-response" {
				command(map[string]any{"action": "lose-next"})
			} else if mode != "normal" {
				command(map[string]any{"action": mode})
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(ready.Certificate)) {
				t.Fatal("missing CA")
			}
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
			defer transport.CloseIdleConnections()
			client := func() *PublicationDescriptionDerivationClient {
				t.Helper()
				c, err := NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{Enabled: true, PlanEnabled: true,
					URL: ready.Origin + publicationDescriptionDerivationPath, Issuer: "https://cats.example", Audience: ready.Origin,
					PeerSecret: "pubderive_" + strings.Repeat("a", 64), Timeout: 10 * time.Second, HTTPClient: &http.Client{Transport: transport}})
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			verifies := 0
			plan := func() (*publicationDescriptionPlannedRelease, error) {
				return planConfirmedPublicationDescription(ctx, db, 1, app.Input.Receipt.Query.EntryID, handoff, false, publicationDescriptionReleasePlanPorts{
					VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { verifies++; return nil }, PlanRetainedCopy: client().PlanRetainedCopy})
			}
			got, err := plan()
			if mode == "cancel" || mode == "publisher" || mode == "disable" {
				if got != nil || err == nil {
					t.Fatal("inactive source accepted", mode)
				}
				stats := command(map[string]any{"action": "stats"})
				if stats["plans"] != float64(0) || stats["reservations"] != float64(0) || stats["commits"] != float64(0) {
					t.Fatal("failed plan mutated release", stats)
				}
				return
			}
			if mode == "lose-response" {
				if got != nil || err == nil {
					t.Fatal("did not lose committed response")
				}
				stats := command(map[string]any{"action": "stats"})
				if stats["plans"] != float64(1) {
					t.Fatal("not committed before response loss", stats)
				}
				got, err = plan()
			}
			if err != nil || got == nil || got.plan.Target.CandidateHash != app.Result.Derivation.DerivedCandidateHash || got.plan.PublicationReceipt != ready.PublicationReceipt {
				t.Fatal("real plan failed", err)
			}
			again, err := plan()
			if err != nil || again == nil || again.plan != got.plan {
				t.Fatal("reconstruction changed target", err)
			}
			stats := command(map[string]any{"action": "stats"})
			if stats["plans"] != float64(1) || stats["reservations"] != float64(1) || stats["commits"] != float64(0) || stats["originalPlans"] != float64(0) || stats["originalUnchanged"] != true || verifies < 4 {
				t.Fatal("duplicated/resumed release", stats)
			}
		})
	}
}
