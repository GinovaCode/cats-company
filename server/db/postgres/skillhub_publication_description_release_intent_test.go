package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func descriptionReleaseIntentMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000060_skillhub_publication_description_release_intent." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func rollbackDescriptionReleaseIntentIfPresent(t *testing.T, db *Adapter) {
	t.Helper()
	var exists bool
	if err := db.db.QueryRow(`SELECT to_regclass('skillhub_publication_description_release_intents') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		deliveryExec(t, db, descriptionReleaseIntentMigration(t, "down"))
	}
}

func TestSkillHubPublicationDescriptionReleaseIntentMigrationMatchesStartup(t *testing.T) {
	b, err := os.ReadFile("../migrations/postgres/000060_skillhub_publication_description_release_intent.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(string(b)), " ") != strings.Join(strings.Fields(createSkillHubPublicationDescriptionReleaseIntentTable), " ") {
		t.Fatal("000060 differs from startup schema")
	}
}

func TestPostgresSkillHubPublicationDescriptionReleaseIntentPersistsExactTarget(t *testing.T) {
	t.Parallel()
	db, dsn, in, repairInput, _, foreign := repairBudgetFixture(t)
	ctx, actor := context.Background(), in.Binding.ActorUID
	app := acceptanceApplication(t, db, actor, repairInput)
	if _, err := db.AcceptSkillHubPublicationDescriptionValidation(ctx, actor, *app, acceptanceReport(*app)); err != nil {
		t.Fatal(err)
	}
	continuation, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, in.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	target := store.SkillHubPublicationCommitTarget{CandidateHash: app.Result.Derivation.DerivedCandidateHash,
		ManifestHash: strings.Repeat("a", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test",
		Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.0", UploadID: "upl_test",
		ReleaseAt: "2026-10-26T01:00:00.000Z", ConfirmVersionPublish: false}
	first, err := db.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *continuation, target)
	if err != nil || first == nil || first.ValidateFor(*continuation) != nil {
		t.Fatal("record exact human-confirmed target", err)
	}
	peer := &Adapter{}
	if err := peer.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	replayed, err := peer.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *continuation, target)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatal("restart did not recover exact release intent", err)
	}
	if got, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, actor, in.EntryID); err != nil || !reflect.DeepEqual(first, got) {
		t.Fatal("exact current-authority read", err)
	}
	if _, err := db.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *continuation, store.SkillHubPublicationCommitTarget{
		CandidateHash: app.Result.Derivation.DerivedCandidateHash, ManifestHash: strings.Repeat("b", 64), PublisherUserID: "usr_test",
		PublisherProfileID: "pub_test", Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.1", UploadID: "upl_other",
		ReleaseAt: "2026-10-26T01:00:01.000Z"}); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("different target replaced immutable release intent", err)
	}
	if _, err := db.GetSkillHubPublicationDescriptionReleaseIntent(ctx, foreign, in.EntryID); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("foreign user read release intent", err)
	}
	for _, q := range []string{
		`UPDATE skillhub_publication_description_release_intents SET target=target WHERE application_id=$1`,
		`DELETE FROM skillhub_publication_description_release_intents WHERE application_id=$1`,
	} {
		if _, err := db.db.Exec(q, app.ID); err == nil {
			t.Fatal("release intent history mutated", q)
		}
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents WHERE entry_id=$1`, in.EntryID).Scan(&count); err != nil || count != 0 {
		t.Fatal("description confirmation was incorrectly turned into the legacy commit intent", err)
	}
}
