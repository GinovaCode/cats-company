package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func uploadTestInput() SkillHubPublicationUploadInput {
	e := publicationTestInput()
	return SkillHubPublicationUploadInput{
		EntryID: "pubentry_" + strings.Repeat("a", 32),
		Binding: SkillHubPublicationDeliveryBinding{ActorUID: e.ActorUID, BotUID: e.BotUID, Source: e.Source, NodeID: "node", ConnectionID: "socket"},
		Issuer:  "https://cats.example", Audience: "https://hub.example:19991", Action: SkillHubPublicationUploadAction,
		Revision: 1, RequestHash: strings.Repeat("b", 64), SourceSnapshotHash: strings.Repeat("c", 64), RequestBytes: 123,
	}
}

func TestSkillHubPublicationUploadInput(t *testing.T) {
	good := uploadTestInput()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SkillHubPublicationUploadInput){
		func(i *SkillHubPublicationUploadInput) { i.EntryID = "invalid" },
		func(i *SkillHubPublicationUploadInput) { i.Binding.ActorUID = i.Binding.BotUID },
		func(i *SkillHubPublicationUploadInput) { i.Binding.Source.RouteGeneration = 0 },
		func(i *SkillHubPublicationUploadInput) { i.Action = "publication.commit" },
		func(i *SkillHubPublicationUploadInput) { i.Revision = 2 },
		func(i *SkillHubPublicationUploadInput) { i.Revision = 0 },
		func(i *SkillHubPublicationUploadInput) { i.RequestHash = strings.Repeat("B", 64) },
		func(i *SkillHubPublicationUploadInput) { i.SourceSnapshotHash = "legacy-hash" },
		func(i *SkillHubPublicationUploadInput) { i.RequestBytes = 0 },
		func(i *SkillHubPublicationUploadInput) { i.RequestBytes = SkillHubPublicationUploadMaxBytes + 1 },
		func(i *SkillHubPublicationUploadInput) { i.Issuer = i.Audience },
	} {
		bad := good
		change(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted invalid input: %+v", bad)
		}
	}
	for _, origin := range []string{"http://cats.example", "https://Cats.example", "https://cats.example/", "https://cats.example/path",
		"https://cats.example?x=1", "https://cats.example#x", "https://user:secret@cats.example",
		"https://cats.example:0", "https://cats.example:65536", "https://cats.example:0443",
		"https://.cats.example", "https://cats..example", "https://-cats.example", "https://cats.example.",
		"https://" + strings.Repeat("a", 64) + ".example", " https://cats.example"} {
		if ValidSkillHubPublicationOrigin(origin) {
			t.Errorf("accepted origin %q", origin)
		}
	}
}

func TestSkillHubPublicationUploadTokenRedaction(t *testing.T) {
	raw := "pubupload_" + strings.Repeat("a", 64)
	token, err := ParseSkillHubPublicationUploadToken(raw)
	if err != nil || token.Reveal() != raw {
		t.Fatal("token roundtrip")
	}
	encoded, err := json.Marshal(struct {
		Token SkillHubPublicationUploadToken
	}{token})
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{fmt.Sprint(token), fmt.Sprintf("%+v", token), fmt.Sprintf("%#v", token), string(encoded)} {
		if strings.Contains(output, raw) || strings.Contains(output, strings.Repeat("a", 64)) {
			t.Fatal("token leaked")
		}
	}
	for _, bad := range []string{"", raw + "0", strings.ToUpper(raw), "legacy-secret", strings.TrimPrefix(raw, "pubupload_")} {
		if _, err := ParseSkillHubPublicationUploadToken(bad); err == nil {
			t.Fatal("invalid bearer accepted")
		}
	}
}
