package server

import (
	"context"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

type publicationDescriptionValidationPorts struct {
	VerifySource         func(context.Context, store.SkillHubPublicationDescriptionApplication) error
	ReadValidationReport func(context.Context, store.SkillHubPublicationDescriptionApplication) (*PublicationValidationReport, error)
}

// A point-in-time internal report, never a browser response or execution grant.
// A future continuation must retain it and recheck authority at its own state
// transition. This lane cannot change a task/application or allocate a round.
type publicationDescriptionValidation struct {
	application store.SkillHubPublicationDescriptionApplication
	report      PublicationValidationReport
}

func snapshotDescriptionValidationReport(report PublicationValidationReport) PublicationValidationReport {
	if report.Issues != nil {
		report.Issues = append([]PublicationValidationIssue{}, report.Issues...)
		for i := range report.Issues {
			if value := report.Issues[i].Location.EntryIndex; value != nil {
				copy := *value
				report.Issues[i].Location.EntryIndex = &copy
			}
		}
	}
	return report
}

func validDescriptionDerivedReport(report PublicationValidationReport, app store.SkillHubPublicationDescriptionApplication) bool {
	return app.Result != nil && validPublicationValidationReport(report, app.Result.Derivation.DerivedCandidateHash) &&
		!report.Truncated && report.Outcome == "passed" && report.LimitsFingerprint == app.Result.Derivation.LimitsFingerprint
}

// Internal, no production HTTP/RPC/model/worker registration. Only the trusted
// peer validates candidate bytes; the caller supplies no report/hash/package.
func validateConfirmedPublicationDescription(ctx context.Context, db publicationDescriptionCopyStore, actor int64, id string, ports publicationDescriptionValidationPorts) (*publicationDescriptionValidation, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || ports.VerifySource == nil || ports.ReadValidationReport == nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.Input.Receipt.Query.EntryID != id {
		return nil, store.ErrSkillHubPublicationConflict
	}
	app := snapshotPublicationDescriptionApplication(*current)
	if err := validatePublicationDescriptionCopyApplication(app); err != nil {
		return nil, err
	}
	checkpoint := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		live, err := db.GetSkillHubPublicationDescriptionApplication(ctx, actor, id)
		if err != nil {
			return err
		}
		if live == nil || validatePublicationDescriptionCopyApplication(*live) != nil || !reflect.DeepEqual(*live, app) {
			return store.ErrSkillHubPublicationConflict
		}
		return ctx.Err()
	}
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ports.VerifySource(ctx, snapshotPublicationDescriptionApplication(app))
	}
	if err := verify(); err != nil {
		return nil, err
	}
	if err := checkpoint(); err != nil {
		return nil, err
	}
	report, err := ports.ReadValidationReport(ctx, snapshotPublicationDescriptionApplication(app))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if report == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	retained := snapshotDescriptionValidationReport(*report)
	if !validDescriptionDerivedReport(retained, app) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := verify(); err != nil {
		return nil, err
	}
	if err := checkpoint(); err != nil {
		return nil, err
	}
	return &publicationDescriptionValidation{application: app, report: retained}, nil
}
