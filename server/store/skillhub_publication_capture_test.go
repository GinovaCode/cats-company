package store

import (
	"strings"
	"testing"
)

func TestSkillHubPublicationCaptureInput(t *testing.T) {
	u := uploadTestInput()
	good := SkillHubPublicationCaptureInput{EntryID: u.EntryID, Binding: u.Binding, Revision: 1, RequestHash: u.RequestHash, SourceSnapshotHash: u.SourceSnapshotHash, RequestBytes: 123}
	if good.Validate() != nil {
		t.Fatal("valid receipt rejected")
	}
	for _, change := range []func(*SkillHubPublicationCaptureInput){
		func(i *SkillHubPublicationCaptureInput) { i.EntryID = "invalid" },
		func(i *SkillHubPublicationCaptureInput) { i.Binding.ActorUID = i.Binding.BotUID },
		func(i *SkillHubPublicationCaptureInput) { i.Binding.Source.RouteGeneration = 0 },
		func(i *SkillHubPublicationCaptureInput) { i.Binding.ConnectionID = "" },
		func(i *SkillHubPublicationCaptureInput) { i.Revision = 0 },
		func(i *SkillHubPublicationCaptureInput) { i.Revision = 2 },
		func(i *SkillHubPublicationCaptureInput) { i.RequestHash = strings.Repeat("A", 64) },
		func(i *SkillHubPublicationCaptureInput) { i.SourceSnapshotHash = "workspace-generation" },
		func(i *SkillHubPublicationCaptureInput) { i.RequestBytes = 0 },
		func(i *SkillHubPublicationCaptureInput) { i.RequestBytes = SkillHubPublicationCaptureMaxBytes + 1 },
	} {
		bad := good
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
	good.RequestBytes = SkillHubPublicationCaptureMaxBytes
	if good.Validate() != nil {
		t.Fatal("boundary rejected")
	}
}

func TestSkillHubPublicationCaptureAndGrantSizeContract(t *testing.T) {
	if SkillHubPublicationCaptureMaxBytes != SkillHubPublicationUploadMaxBytes {
		t.Fatal("capture and upload authority limits diverged")
	}
	for _, size := range []int64{1, 32 * 1024 * 1024, 32*1024*1024 + 1, 75 * 1024 * 1024, 75*1024*1024 + 1} {
		u := uploadTestInput()
		u.RequestBytes = size
		c := SkillHubPublicationCaptureInput{EntryID: u.EntryID, Binding: u.Binding, Revision: u.Revision,
			RequestHash: u.RequestHash, SourceSnapshotHash: u.SourceSnapshotHash, RequestBytes: size}
		want := size <= 75*1024*1024
		if (c.Validate() == nil) != want || (u.Validate() == nil) != want || (u.Query(1).Validate() == nil) != want {
			t.Fatalf("capture/grant/introspection disagree at %d bytes", size)
		}
	}
}
