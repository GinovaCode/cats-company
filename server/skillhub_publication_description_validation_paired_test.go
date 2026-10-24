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

// Real Cloud HTTPS handler/validator/worker/derivation and real CatsCo client /
// coordinator. Cloud retention, CatsCo authority and Runtime source are synthetic.
// The strict runner pins Cloud, requires these cases and rejects all skips.
func TestPublicationDescriptionValidationPairedCloud(t *testing.T) {
	root, node := os.Getenv("SKILLHUB_DESCRIPTION_PAIRED_ROOT"), os.Getenv("SKILLHUB_DESCRIPTION_PAIRED_NODE")
	if root == "" || node == "" {
		t.Skip("use scripts/test-publication-description-validation-paired.mjs for the mandatory pinned contract")
	}
	for _, mode := range []string{"normal", "cancel", "change-copy", "disable"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			cmd := exec.CommandContext(ctx, node, filepath.Join("..", "scripts", "fixtures", "publication-description-validation-cloud.mjs"), root)
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
					t.Errorf("Cloud fixture did not exit: %s", stderr.String())
				}
			})
			decoder := json.NewDecoder(io.LimitReader(out, 256*1024))
			var ready struct {
				Origin             string                      `json:"origin"`
				Certificate        string                      `json:"certificate"`
				OriginalBytes      int64                       `json:"originalBytes"`
				OriginalValidation PublicationValidationReport `json:"originalValidation"`
				Request            struct {
					TaskID          string                                                `json:"taskId"`
					RequestHash     string                                                `json:"requestHash"`
					CandidateHash   string                                                `json:"candidateHash"`
					Proposal        store.SkillHubPublicationDescriptionProposal          `json:"proposal"`
					ExpectedReceipt store.SkillHubPublicationDescriptionDerivationReceipt `json:"expectedReceipt"`
					DerivedBytes    int64                                                 `json:"derivedBytes"`
				} `json:"request"`
			}
			if err := decoder.Decode(&ready); err != nil {
				t.Fatal("Cloud readiness", err)
			}
			app := completedDescriptionApplication(t, ready.Origin, []byte("placeholder"))
			app.Input.Receipt.Query.Issuer = "https://cats.example"
			app.Input.Receipt.Query.RequestHash = ready.Request.RequestHash
			app.Input.Receipt.Query.SourceSnapshotHash = ready.Request.CandidateHash
			app.Input.Receipt.Query.RequestBytes = ready.OriginalBytes
			app.Input.Receipt.TaskID = ready.Request.TaskID
			app.Input.Draft.Proposal = ready.Request.Proposal
			app.Input.Draft.Evidence, err = publicationRepairEvidence(ready.OriginalValidation, ready.Request.CandidateHash)
			if err != nil {
				t.Fatal("real original Cloud report", err)
			}
			app.Result.Derivation = ready.Request.ExpectedReceipt
			app.Result.RequestBytes = ready.Request.DerivedBytes
			app = repairSourceApplication(t, app)
			app = repairSourceApplicationAtRound(t, app, 1) // Real store ValidateFor, not a self-validating hash fixture.
			command := func(value any) {
				t.Helper()
				if err := json.NewEncoder(in).Encode(value); err != nil {
					t.Fatal(err)
				}
				var ack struct {
					OK bool `json:"ok"`
				}
				if err := decoder.Decode(&ack); err != nil || !ack.OK {
					t.Fatal("Cloud control", err)
				}
			}
			command(map[string]any{"action": "bind", "applicationId": app.ID})
			if mode != "normal" {
				command(map[string]any{"action": mode})
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(ready.Certificate)) {
				t.Fatal("missing test CA")
			}
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
			defer transport.CloseIdleConnections()
			client, err := NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{Enabled: true, ValidationEnabled: true,
				URL: ready.Origin + publicationDescriptionDerivationPath, Issuer: "https://cats.example", Audience: ready.Origin,
				PeerSecret: "pubderive_" + strings.Repeat("a", 64), Timeout: 10 * time.Second, HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			db := &descriptionCopyTestStore{app: &app}
			verifies := 0
			ports := publicationDescriptionValidationPorts{VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { verifies++; return nil }, ReadValidationReport: client.ReadValidationReport}
			got, err := validateConfirmedPublicationDescription(ctx, db, 1, app.Input.Receipt.Query.EntryID, ports)
			if mode != "normal" {
				if err == nil || got != nil {
					t.Fatal("Cloud failure accepted", mode)
				}
				return
			}
			if err != nil || got == nil || got.report.CandidateHash != ready.Request.ExpectedReceipt.DerivedCandidateHash || got.report.Outcome != "passed" || verifies != 2 || db.get != 3 {
				t.Fatal("real peer validation failed", err)
			}
			first := got.report.ReportID
			got, err = validateConfirmedPublicationDescription(ctx, db, 1, app.Input.Receipt.Query.EntryID, ports)
			if err != nil || got == nil || got.report.ReportID == first {
				t.Fatal("fresh report/recreated request failed", err)
			}
		})
	}
}
