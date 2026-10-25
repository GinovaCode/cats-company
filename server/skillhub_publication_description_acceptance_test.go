package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type descriptionAcceptanceTestStore struct {
	descriptionCopyTestStore
	accepted *store.SkillHubPublicationDescriptionAcceptance
	onRead   func() error
	onAccept func() error
	writes   int
}

func (db *descriptionAcceptanceTestStore) GetSkillHubPublicationDescriptionAcceptance(_ context.Context, _ int64, app store.SkillHubPublicationDescriptionApplication) (*store.SkillHubPublicationDescriptionAcceptance, error) {
	if db.onRead != nil {
		if err := db.onRead(); err != nil {
			return nil, err
		}
	}
	if db.app == nil || !reflect.DeepEqual(*db.app, app) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if db.accepted == nil {
		return nil, nil
	}
	a := snapshotDescriptionAcceptance(*db.accepted)
	return &a, nil
}

func (db *descriptionAcceptanceTestStore) AcceptSkillHubPublicationDescriptionValidation(_ context.Context, _ int64, app store.SkillHubPublicationDescriptionApplication, r store.PublicationValidationReport) (*store.SkillHubPublicationDescriptionAcceptance, error) {
	db.writes++
	if db.onAccept != nil {
		if err := db.onAccept(); err != nil {
			return nil, err
		}
	}
	if db.app == nil || !reflect.DeepEqual(*db.app, app) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	a := store.SkillHubPublicationDescriptionAcceptance{ApplicationID: app.ID, Result: *app.Result, Report: snapshotDescriptionValidationReport(r), AcceptedAt: time.Now().UTC()}
	a.IdentityHash, _ = a.Digest()
	if err := a.ValidateFor(app); err != nil {
		return nil, err
	}
	if db.accepted != nil && db.accepted.IdentityHash != a.IdentityHash {
		return nil, store.ErrSkillHubPublicationConflict
	}
	db.accepted = &a
	return &a, nil
}

func acceptanceTestPorts(app store.SkillHubPublicationDescriptionApplication, reads *int) publicationDescriptionValidationPorts {
	return publicationDescriptionValidationPorts{
		VerifySource: func(context.Context, store.SkillHubPublicationDescriptionApplication) error { return nil },
		ReadValidationReport: func(context.Context, store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
			*reads++
			r := descriptionDerivedReport(app)
			return &r, nil
		},
	}
}

func TestAcceptConfirmedPublicationDescriptionRecovery(t *testing.T) {
	app := completedDescriptionApplication(t, "https://hub.example", []byte("private-copy"))
	before := snapshotPublicationDescriptionApplication(app)
	db := &descriptionAcceptanceTestStore{descriptionCopyTestStore: descriptionCopyTestStore{app: &app}}
	reads := 0
	ports := acceptanceTestPorts(app, &reads)
	ports.ReadValidationReport = func(context.Context, store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
		reads++
		r := descriptionDerivedReport(app)
		index := int64(0)
		r.Issues = []PublicationValidationIssue{{Code: "metadata.warning", Category: "compatibility", Message: "warning", RepairPolicy: "none",
			Location: PublicationValidationLocation{EntryIndex: &index, RelativePath: "SKILL.md"}}}
		return &r, nil
	}
	got, err := acceptConfirmedPublicationDescription(context.Background(), db, 1, app.Input.Receipt.Query.EntryID, ports)
	if err != nil || got == nil || reads != 1 || db.writes != 1 {
		t.Fatal("first accept", err, reads, db.writes)
	}
	peer := &descriptionAcceptanceTestStore{descriptionCopyTestStore: descriptionCopyTestStore{app: &app}, accepted: db.accepted}
	ports.ReadValidationReport = func(context.Context, store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error) {
		t.Fatal("recovery fetched new report")
		return nil, nil
	}
	again, err := acceptConfirmedPublicationDescription(context.Background(), peer, 1, app.Input.Receipt.Query.EntryID, ports)
	if err != nil || !reflect.DeepEqual(again, got) || peer.writes != 0 || !reflect.DeepEqual(app, before) {
		t.Fatal("lost reply changed history", err)
	}
	got.accepted.Report.ReportID = "changed"
	got.accepted.Report.Issues[0].Message = "changed"
	*got.accepted.Report.Issues[0].Location.EntryIndex = 99
	got.application.Result.RequestBytes++
	*got.application.Input.Draft.Proposal.EntryIndex = 99
	if peer.accepted.Report.ReportID == "changed" || peer.accepted.Report.Issues[0].Message != "warning" ||
		*peer.accepted.Report.Issues[0].Location.EntryIndex != 0 || !reflect.DeepEqual(app, before) {
		t.Fatal("report alias")
	}
}

func TestAcceptConfirmedPublicationDescriptionCheckpoints(t *testing.T) {
	for _, kind := range []string{"read_denied", "source_before", "source_after_persist", "write_denied", "changed_application", "cancel_at_write", "invalid_retained", "race_winner"} {
		t.Run(kind, func(t *testing.T) {
			app := completedDescriptionApplication(t, "https://hub.example", []byte("copy"))
			db := &descriptionAcceptanceTestStore{descriptionCopyTestStore: descriptionCopyTestStore{app: &app}}
			reads, sources := 0, 0
			ports := acceptanceTestPorts(app, &reads)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ports.VerifySource = func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
				sources++
				if kind == "source_before" || (kind == "source_after_persist" && sources == 3) {
					return store.ErrSkillHubPublicationStale
				}
				return nil
			}
			if kind == "read_denied" {
				db.onRead = func() error { return store.ErrSkillHubPublicationDenied }
			}
			if kind == "write_denied" {
				db.onAccept = func() error { return store.ErrSkillHubPublicationDenied }
			}
			if kind == "changed_application" {
				db.onAccept = func() error { app.Result.RequestBytes++; return nil }
			}
			if kind == "cancel_at_write" {
				db.onAccept = func() error { cancel(); return context.Canceled }
			}
			if kind == "invalid_retained" {
				db.accepted = &store.SkillHubPublicationDescriptionAcceptance{ApplicationID: app.ID}
			}
			if kind == "race_winner" {
				db.onAccept = func() error {
					r := descriptionDerivedReport(app)
					r.ReportID = "22345678-1234-4123-8123-123456789abc"
					a := store.SkillHubPublicationDescriptionAcceptance{ApplicationID: app.ID, Result: *app.Result, Report: r, AcceptedAt: time.Now().UTC()}
					a.IdentityHash, _ = a.Digest()
					db.accepted = &a
					return store.ErrSkillHubPublicationConflict
				}
			}
			got, err := acceptConfirmedPublicationDescription(ctx, db, 1, app.Input.Receipt.Query.EntryID, ports)
			if kind == "race_winner" {
				if err != nil || got == nil || got.accepted.Report.ReportID != db.accepted.Report.ReportID || reads != 1 {
					t.Fatal("winner not recovered", err)
				}
				return
			}
			if err == nil || got != nil {
				t.Fatal("unsafe checkpoint accepted")
			}
			if (kind == "read_denied" || kind == "source_before" || kind == "invalid_retained") && (db.writes != 0 || reads != 0) {
				t.Fatal("invalid checkpoint reached peer/write")
			}
		})
	}
}

func TestAcceptConfirmedPublicationDescriptionReplayRechecksAuthority(t *testing.T) {
	app := completedDescriptionApplication(t, "https://hub.example", []byte("copy"))
	reads := 0
	db := &descriptionAcceptanceTestStore{descriptionCopyTestStore: descriptionCopyTestStore{app: &app}}
	ports := acceptanceTestPorts(app, &reads)
	if _, err := acceptConfirmedPublicationDescription(context.Background(), db, 1, app.Input.Receipt.Query.EntryID, ports); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"source", "authority", "record"} {
		t.Run(kind, func(t *testing.T) {
			peer := &descriptionAcceptanceTestStore{descriptionCopyTestStore: descriptionCopyTestStore{app: &app}, accepted: db.accepted}
			p := ports
			p.VerifySource = func(context.Context, store.SkillHubPublicationDescriptionApplication) error {
				if kind == "source" {
					return store.ErrSkillHubPublicationStale
				}
				if kind == "authority" {
					peer.onGet = func(int) error { return store.ErrSkillHubPublicationDenied }
				}
				if kind == "record" {
					peer.accepted = nil
				}
				return nil
			}
			if got, err := acceptConfirmedPublicationDescription(context.Background(), peer, 1, app.Input.Receipt.Query.EntryID, p); err == nil || got != nil {
				t.Fatal("replay bypassed current checks")
			}
		})
	}
	if reads != 1 {
		t.Fatal("replay called peer")
	}
	if _, err := acceptConfirmedPublicationDescription(context.Background(), nil, 1, app.Input.Receipt.Query.EntryID, ports); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		t.Fatal(err)
	}
}
