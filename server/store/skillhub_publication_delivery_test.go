package store

import (
	"strings"
	"testing"
	"time"
)

func TestSkillHubPublicationDeliveryValidation(t *testing.T) {
	b := SkillHubPublicationDeliveryBinding{ActorUID: 1, BotUID: 2, Source: publicationTestInput().Source, NodeID: "node-1", ConnectionID: "connection-1"}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SkillHubPublicationDeliveryBinding){
		func(b *SkillHubPublicationDeliveryBinding) { b.ActorUID = 0 },
		func(b *SkillHubPublicationDeliveryBinding) { b.BotUID = b.ActorUID },
		func(b *SkillHubPublicationDeliveryBinding) { b.NodeID = "" },
		func(b *SkillHubPublicationDeliveryBinding) { b.ConnectionID = strings.Repeat("x", 129) },
		func(b *SkillHubPublicationDeliveryBinding) { b.Source.InstallationID = "/root/skills" },
		func(b *SkillHubPublicationDeliveryBinding) { b.Source.RouteGeneration = 0 },
	} {
		bad := b
		change(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if !ValidSkillHubPublicationDeliveryEntryID("pubentry_"+strings.Repeat("a", 32)) || ValidSkillHubPublicationDeliveryEntryID("pubentry_bad") {
		t.Fatal("entry ID")
	}
	if !ValidSkillHubPublicationDeliveryToken(strings.Repeat("b", 32)) || ValidSkillHubPublicationDeliveryToken(strings.Repeat("B", 32)) {
		t.Fatal("lease token")
	}
}

func TestSkillHubPublicationDeliveryRetryPolicy(t *testing.T) {
	for attempt, seconds := range map[int]int{-1: 5, 0: 5, 1: 5, 2: 10, 3: 20, 4: 40, 5: 80, 6: 160, 7: 300, 8: 300, 9999: 300} {
		if got := SkillHubPublicationDeliveryBackoff(attempt); got != time.Duration(seconds)*time.Second {
			t.Fatalf("%d: %v", attempt, got)
		}
	}
	for code, state := range map[string]string{"": "source_verified", "publication.runtime_timeout": "pending", "publication.runtime_unavailable": "pending", "publication.source_missing": "needs_user_action", "publication.source_changed": "needs_user_action", "publication.source_unsafe": "needs_user_action", "publication.runtime_unsupported": "needs_user_action"} {
		if got, ok := SkillHubPublicationDeliveryOutcome(code); !ok || got != state {
			t.Fatalf("%s: %s %v", code, got, ok)
		}
	}
	for _, code := range []string{"raw error /root/private", "publication.delivery_exhausted", "published", "ready", "publication.authority_unavailable"} {
		if _, ok := SkillHubPublicationDeliveryOutcome(code); ok {
			t.Fatalf("accepted %s", code)
		}
	}
}
