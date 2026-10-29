package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func TestValidateConfirmedPublicationDescriptionReadOnly(t *testing.T) {
	app := completedDescriptionApplication(t, "https://hub.example", []byte("private-derived"))
	before := snapshotPublicationDescriptionApplication(app)
	db := &descriptionCopyTestStore{app: &app}
	report := descriptionDerivedReport(app)
	index := int64(0)
	report.Issues = []PublicationValidationIssue{{Code: "metadata.warning", Category: "compatibility", Blocking: false,
		Location: PublicationValidationLocation{EntryIndex: &index, RelativePath: "SKILL.md"}, Message: "warning", RepairPolicy: "none"}}
	baseline := snapshotDescriptionValidationReport(report)
	var events []string
	peerReturned := false
	db.onGet = func(int) error { events = append(events, "authority"); return nil }
	ports := publicationDescriptionValidationPorts{
		VerifySource: func(_ context.Context, a store.SkillHubPublicationDescriptionApplication) error {
			events = append(events, "source")
			if !reflect.DeepEqual(a, before) {
				t.Fatal("source binding changed")
			}
			*a.Input.Draft.Proposal.EntryIndex = 99
			a.Result.RequestBytes++
			if peerReturned {
				report.Issues[0].Message = "changed"
				*report.Issues[0].Location.EntryIndex = 99
			}
			return nil
		},
		ReadValidationReport: func(_ context.Context, a store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
			events = append(events, "report")
			if !reflect.DeepEqual(a, before) {
				t.Fatal("peer binding changed")
			}
			peerReturned = true
			return &report, nil
		},
	}
	got, err := validateConfirmedPublicationDescription(context.Background(), db, 1, app.Input.Receipt.Query.EntryID, ports)
	if err != nil || got == nil || !reflect.DeepEqual(got.application, before) || !reflect.DeepEqual(got.report, baseline) {
		t.Fatal("report/binding changed", err)
	}
	if !reflect.DeepEqual(app, before) {
		t.Fatal("validation mutated application/budget")
	}
	if !reflect.DeepEqual(events, []string{"authority", "source", "authority", "report", "source", "authority"}) {
		t.Fatal("wrong checkpoint order", events)
	}
	got.report.Issues[0].Message = "caller-changed"
	*got.report.Issues[0].Location.EntryIndex = 88
	if report.Issues[0].Message != "changed" || *report.Issues[0].Location.EntryIndex != 99 {
		t.Fatal("returned report aliases peer")
	}
}

func TestValidateConfirmedPublicationDescriptionRejectsCheckpoints(t *testing.T) {
	for _, step := range []int{1, 2, 3} {
		for _, kind := range []string{"denied", "missing", "incomplete", "changed-result", "changed-confirmation"} {
			t.Run(string(rune('0'+step))+"/"+kind, func(t *testing.T) {
				app := completedDescriptionApplication(t, "https://hub.example", []byte("private-copy"))
				db := &descriptionCopyTestStore{app: &app}
				id := app.Input.Receipt.Query.EntryID
				peer, source := 0, 0
				db.onGet = func(n int) error {
					if n != step {
						return nil
					}
					switch kind {
					case "denied":
						return store.ErrSkillHubPublicationDenied
					case "missing":
						db.app = nil
					case "incomplete":
						app.Result = nil
						app.AppliedAt = nil
					case "changed-result":
						app.Result.Derivation.LimitsFingerprint = strings.Repeat("f", 64)
					case "changed-confirmation":
						app.Input.ConfirmationKey = strings.Repeat("f", 64)
					}
					return nil
				}
				ports := publicationDescriptionValidationPorts{
					VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { source++; return nil },
					ReadValidationReport: func(_ context.Context, a store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
						peer++
						r := descriptionDerivedReport(a)
						return &r, nil
					},
				}
				got, err := validateConfirmedPublicationDescription(context.Background(), db, 1, id, ports)
				// At the initial authoritative read a self-consistent retained
				// result is the baseline, rather than caller-supplied authority.
				validInitial := step == 1 && kind == "changed-result"
				if validInitial {
					if err != nil || got == nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil || got != nil {
					t.Fatal("changed/denied checkpoint accepted")
				}
				if step < 3 && peer != 0 {
					t.Fatal("invalid initial authority reached peer")
				}
				if step == 1 && source != 0 {
					t.Fatal("invalid authority reached source")
				}
			})
		}
	}
}

func TestValidateConfirmedPublicationDescriptionRejectsReportsAndSource(t *testing.T) {
	for _, kind := range []string{"nil", "peer-error", "original-candidate", "limits", "truncated", "rejected", "source-before", "source-after", "cancel-before", "cancel-peer", "cancel-final"} {
		t.Run(kind, func(t *testing.T) {
			app := completedDescriptionApplication(t, "https://hub.example", []byte("private-copy"))
			db := &descriptionCopyTestStore{app: &app}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancel-before" {
				cancel()
			}
			verifies := 0
			ports := publicationDescriptionValidationPorts{
				VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
					verifies++
					if kind == "source-before" || (kind == "source-after" && verifies == 2) {
						return store.ErrSkillHubPublicationStale
					}
					if kind == "cancel-final" && verifies == 2 {
						cancel()
					}
					return nil
				},
				ReadValidationReport: func(_ context.Context, a store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
					r := descriptionDerivedReport(a)
					switch kind {
					case "nil":
						return nil, nil
					case "peer-error":
						return nil, errors.New("peer unavailable")
					case "original-candidate":
						r.CandidateHash = a.Input.Receipt.Query.SourceSnapshotHash
					case "limits":
						r.LimitsFingerprint = strings.Repeat("f", 64)
					case "truncated":
						r.Truncated = true
					case "rejected":
						r.Outcome = "rejected"
						r.NextAction = "review_report"
						r.Truncated = true
					case "cancel-peer":
						cancel()
					}
					return &r, nil
				},
			}
			got, err := validateConfirmedPublicationDescription(ctx, db, 1, app.Input.Receipt.Query.EntryID, ports)
			if err == nil || got != nil {
				t.Fatal("unsafe report/source accepted")
			}
		})
	}
}

func TestValidateConfirmedPublicationDescriptionMissingAdapters(t *testing.T) {
	app := completedDescriptionApplication(t, "https://hub.example", []byte("private-copy"))
	db := &descriptionCopyTestStore{app: &app}
	ports := publicationDescriptionValidationPorts{}
	if got, err := validateConfirmedPublicationDescription(context.Background(), db, 1, app.Input.Receipt.Query.EntryID, ports); got != nil || !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		t.Fatal(err)
	}
	if validDescriptionDerivedReport(descriptionDerivedReport(app), store.SkillHubPublicationDescriptionApplication{}) {
		t.Fatal("report without result accepted")
	}
}
