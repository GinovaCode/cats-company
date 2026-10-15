package store

import (
	"errors"
	"strings"
	"testing"
)

func publicationTestInput() SkillHubPublicationEntryInput {
	return SkillHubPublicationEntryInput{ActorUID: 1, BotUID: 2, RequestID: "request-0001", Entrypoint: "workspace",
		Source: SkillHubPublicationSource{DeviceID: "device:1", BodyID: "body.1", InstallationID: "install_1",
			RouteGeneration: 1, WorkspaceGeneration: "rev-1", LocalSkillID: "local:skill-1"}}
}

func TestSkillHubPublicationEntryIdentity(t *testing.T) {
	original := publicationTestInput()
	hash, err := original.IdentityHash()
	if err != nil || len(hash) != 64 {
		t.Fatalf("hash=%q error=%v", hash, err)
	}
	if again, _ := original.IdentityHash(); again != hash {
		t.Fatal("unstable identity")
	}
	changes := []func(*SkillHubPublicationEntryInput){
		func(in *SkillHubPublicationEntryInput) { in.ActorUID = 3 },
		func(in *SkillHubPublicationEntryInput) { in.BotUID = 3 },
		func(in *SkillHubPublicationEntryInput) { in.RequestID = "request-0002" },
		func(in *SkillHubPublicationEntryInput) { in.Source.DeviceID = "device-2" },
		func(in *SkillHubPublicationEntryInput) { in.Source.BodyID = "body-2" },
		func(in *SkillHubPublicationEntryInput) { in.Source.InstallationID = "install-2" },
		func(in *SkillHubPublicationEntryInput) { in.Source.RouteGeneration++ },
		func(in *SkillHubPublicationEntryInput) { in.Source.WorkspaceGeneration = "rev-2" },
		func(in *SkillHubPublicationEntryInput) { in.Source.LocalSkillID = "skill-2" },
	}
	for i, change := range changes {
		changed := original
		change(&changed)
		if got, err := changed.IdentityHash(); err != nil || got == hash {
			t.Fatalf("change %d not bound: %v", i, err)
		}
	}
}

func TestSkillHubPublicationEntryRejectsInvalidInput(t *testing.T) {
	changes := []func(*SkillHubPublicationEntryInput){
		func(in *SkillHubPublicationEntryInput) { in.ActorUID = 0 },
		func(in *SkillHubPublicationEntryInput) { in.BotUID = in.ActorUID },
		func(in *SkillHubPublicationEntryInput) { in.RequestID = "short" },
		func(in *SkillHubPublicationEntryInput) { in.RequestID = "request:1" },
		func(in *SkillHubPublicationEntryInput) { in.RequestID = strings.Repeat("a", 129) },
		func(in *SkillHubPublicationEntryInput) { in.Entrypoint = "chat" },
		func(in *SkillHubPublicationEntryInput) { in.Source.RouteGeneration = 0 },
		func(in *SkillHubPublicationEntryInput) { in.Source.DeviceID = "" },
		func(in *SkillHubPublicationEntryInput) { in.Source.BodyID = "/root/skill" },
		func(in *SkillHubPublicationEntryInput) { in.Source.InstallationID = "Bearer token" },
		func(in *SkillHubPublicationEntryInput) { in.Source.LocalSkillID = "../skill" },
		func(in *SkillHubPublicationEntryInput) { in.Source.LocalSkillID = "C:\\skills" },
		func(in *SkillHubPublicationEntryInput) { in.Source.WorkspaceGeneration = "rev\x00x" },
		func(in *SkillHubPublicationEntryInput) { in.Source.WorkspaceGeneration = strings.Repeat("x", 129) },
	}
	for i, change := range changes {
		in := publicationTestInput()
		change(&in)
		if _, err := in.IdentityHash(); !errors.Is(err, ErrSkillHubPublicationInvalid) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

func TestSkillHubPublicationEntryPolicy(t *testing.T) {
	if err := DefaultSkillHubPublicationEntryPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []SkillHubPublicationEntryPolicy{{}, {1, 2, 1}, {100001, 1, 1}, {10000, 1001, 1}, {10, 1, 601}, {10, 1, -1}} {
		if !errors.Is(p.Validate(), ErrSkillHubPublicationInvalid) {
			t.Fatalf("accepted %+v", p)
		}
	}
}
