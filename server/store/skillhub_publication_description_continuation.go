package store

import (
	"context"
	"reflect"
)

// A current-authority view of retained records, not another mutable round or
// an execution/commit permit. Task.State remains waiting_confirmation; the
// accepted derived candidate is validated without rewriting that history.
type SkillHubPublicationDescriptionContinuation struct {
	Task        SkillHubPublicationRepairTask
	Draft       SkillHubPublicationDescriptionDraft
	Application SkillHubPublicationDescriptionApplication
	Acceptance  SkillHubPublicationDescriptionAcceptance
}

func (c SkillHubPublicationDescriptionContinuation) Validate() error {
	t := c.Task
	if t.CreatedAt.IsZero() || t.CreatedAt.After(c.Draft.CreatedAt) || len(t.Rounds) == 0 ||
		len(t.Rounds) > SkillHubPublicationRepairMaxRounds || c.Application.ValidateFor(t, c.Draft) != nil ||
		!reflect.DeepEqual(c.Application.Input.Draft, c.Draft.Input) ||
		c.Acceptance.ValidateFor(c.Application) != nil {
		return ErrSkillHubPublicationConflict
	}
	// Validate the whole retained chain, including earlier rounds, rather than
	// treating a valid final draft hash as proof of budget/progress history.
	prior := SkillHubPublicationRepairTask{Input: t.Input}
	expected := t.Input.Evidence
	for i, r := range t.Rounds {
		if r.Lease.Validate() != nil || r.Lease.EntryID != c.Draft.EntryID || r.Lease.TaskID != t.ID ||
			r.Lease.Round != i+1 || r.Lease.Input.Evidence != expected || r.Result == nil || r.Result.Validate() != nil {
			return ErrSkillHubPublicationConflict
		}
		prior.Rounds = append(prior.Rounds, r)
		if i < len(t.Rounds)-1 {
			if prior.State() != "queued" {
				return ErrSkillHubPublicationConflict
			}
			expected = r.Result.Evidence
		}
	}
	return nil
}

// One transaction reads the task, exact confirmed draft, completed application
// and accepted report under current entry/private-task authority locks. No
// writes, network calls, round allocation or in-memory fallback belong here.
type SkillHubPublicationDescriptionContinuationStore interface {
	GetSkillHubPublicationDescriptionContinuation(context.Context, int64, string) (*SkillHubPublicationDescriptionContinuation, error)
}
