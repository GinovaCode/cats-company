package store

import (
	"strings"
	"testing"
	"time"
)

func TestSkillHubPublicationDescriptionReleaseIntentDigestBindsHumanAndTarget(t *testing.T) {
	c := descriptionContinuationFixture(t)
	i := SkillHubPublicationDescriptionReleaseIntent{EntryID: c.Draft.EntryID, ActorUID: 42, ApplicationID: c.Application.ID,
		AcceptanceIdentityHash: c.Acceptance.IdentityHash, Target: SkillHubPublicationCommitTarget{CandidateHash: c.Application.Result.Derivation.DerivedCandidateHash,
			ManifestHash: strings.Repeat("e", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test", Namespace: "publisher",
			SkillID: "publisher/example", Version: "1.0.0", UploadID: "upl_test", ReleaseAt: "2026-10-26T01:00:00.000Z"},
		RequestedAt: c.Acceptance.AcceptedAt.Add(time.Second)}
	h, err := i.Digest()
	if err != nil {
		t.Fatal(err)
	}
	i.IdentityHash = h
	if err := i.ValidateFor(c); err != nil {
		t.Fatal(err)
	}
	changed := i
	changed.Target.Version = "1.0.1"
	other, err := changed.Digest()
	if err != nil || other == h {
		t.Fatalf("target mutation did not change the intent digest: %q %v", other, err)
	}
	changed = i
	changed.ActorUID++
	other, err = changed.Digest()
	if err != nil || other == h {
		t.Fatalf("actor mutation did not change the intent digest: %q %v", other, err)
	}
	changed = i
	changed.Target.CandidateHash = strings.Repeat("f", 64)
	changed.IdentityHash = h
	if err := changed.ValidateFor(c); err == nil {
		t.Fatal("intent for a different derived candidate was accepted")
	}
	changed = i
	changed.RequestedAt = c.Acceptance.AcceptedAt.Add(-time.Second)
	if err := changed.ValidateFor(c); err == nil {
		t.Fatal("intent predating report acceptance was accepted")
	}
}
