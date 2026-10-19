package store

import (
	"reflect"
	"strings"
	"testing"
)

func commitTestInput() SkillHubPublicationCommitInput {
	return SkillHubPublicationCommitInput{
		Receipt: SkillHubPublicationCloudReceipt{
			Query: SkillHubPublicationUploadQuery{EntryID: "pubentry_" + strings.Repeat("a", 32), Issuer: "https://cats.example", Audience: "https://hub.example", Action: SkillHubPublicationUploadAction,
				Revision: 1, Epoch: 1, RequestHash: strings.Repeat("a", 64), SourceSnapshotHash: strings.Repeat("b", 64), RequestBytes: 100},
			DelegationID: "pubgrant_" + strings.Repeat("c", 32), IdempotencyKey: "pubcandidate_" + strings.Repeat("d", 32),
			TaskID: "pubtask_12345678-1234-4234-8234-123456789abc", EventID: "pubevent_12345678-1234-4234-8234-123456789abc"},
		Target: SkillHubPublicationCommitTarget{CandidateHash: strings.Repeat("c", 64), ManifestHash: strings.Repeat("d", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test", Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.1", UploadID: "upl_test", ReleaseAt: "2026-10-19T00:00:00.000Z", ConfirmVersionPublish: true},
	}
}

func TestSkillHubPublicationCommitValidation(t *testing.T) {
	in := commitTestInput()
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*SkillHubPublicationCommitInput){
		"receipt":           func(i *SkillHubPublicationCommitInput) { i.Receipt.Query.Epoch = 0 },
		"candidate":         func(i *SkillHubPublicationCommitInput) { i.Target.CandidateHash = "" },
		"manifest":          func(i *SkillHubPublicationCommitInput) { i.Target.ManifestHash = "x" },
		"publisher":         func(i *SkillHubPublicationCommitInput) { i.Target.PublisherUserID = "../user" },
		"profile":           func(i *SkillHubPublicationCommitInput) { i.Target.PublisherProfileID = "" },
		"namespace":         func(i *SkillHubPublicationCommitInput) { i.Target.Namespace = "someone-else" },
		"skill":             func(i *SkillHubPublicationCommitInput) { i.Target.SkillID = "publisher/a/b" },
		"version":           func(i *SkillHubPublicationCommitInput) { i.Target.Version = "latest" },
		"upload":            func(i *SkillHubPublicationCommitInput) { i.Target.UploadID = "" },
		"release timezone":  func(i *SkillHubPublicationCommitInput) { i.Target.ReleaseAt = "2026-10-19T00:00:00.000+00:00" },
		"release precision": func(i *SkillHubPublicationCommitInput) { i.Target.ReleaseAt = "2026-10-19T00:00:00Z" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := in
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("accepted invalid intent")
			}
		})
	}
}

func TestSkillHubPublicationCommitHashBindsEveryTargetField(t *testing.T) {
	in := commitTestInput()
	want, err := in.IdentityHash()
	if err != nil {
		t.Fatal(err)
	}
	again, _ := in.IdentityHash()
	if again != want {
		t.Fatal("unstable identity")
	}
	for n := 0; n < reflect.TypeOf(in.Target).NumField(); n++ {
		changed := in
		field := reflect.ValueOf(&changed.Target).Elem().Field(n)
		if field.Kind() == reflect.Bool {
			field.SetBool(false)
		} else {
			field.SetString(field.String() + "x")
		}
		got, err := changed.IdentityHash()
		if err == nil && got == want {
			t.Fatalf("unbound field %s", reflect.TypeOf(in.Target).Field(n).Name)
		}
	}
	changed := in
	changed.Receipt.Query.RequestBytes++
	got, err := changed.IdentityHash()
	if err != nil || got == want {
		t.Fatal("receipt not bound")
	}
}
