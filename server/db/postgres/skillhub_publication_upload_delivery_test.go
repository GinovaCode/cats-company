package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func uploadDeliveryFixture(t *testing.T) (*Adapter, store.SkillHubPublicationCaptureInput) {
	t.Helper()
	db, _, in, _ := captureFixture(t)
	captureLease := captureDeliveryClaim(t, db, in)
	recordCaptureForTest(t, db, in)
	if err := db.FinishSkillHubPublicationCaptureDelivery(context.Background(), *captureLease, "captured"); err != nil {
		t.Fatal(err)
	}
	return db, in
}

func uploadDeliveryClaim(t *testing.T, db *Adapter, in store.SkillHubPublicationCaptureInput) *store.SkillHubPublicationUploadLease {
	t.Helper()
	l, err := db.ClaimSkillHubPublicationUploadDelivery(context.Background(), in.EntryID, in.Binding)
	if err != nil || l == nil || l.Validate() != nil {
		t.Fatalf("upload claim: %+v %v", l, err)
	}
	return l
}

func uploadDeliveryIDs(t *testing.T, db *Adapter, in store.SkillHubPublicationCaptureInput) []string {
	t.Helper()
	b := in.Binding
	ids, err := db.ListSkillHubPublicationUploadDeliveries(context.Background(), store.SkillHubPublicationRuntimeSelector{
		ActorUID: in.Binding.ActorUID, BotUID: in.Binding.BotUID, DeviceID: b.Source.DeviceID,
		BodyID: b.Source.BodyID, InstallationID: b.Source.InstallationID,
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestPostgresSkillHubPublicationUploadDeliveryRecoveryAndBudget(t *testing.T) {
	db, in := uploadDeliveryFixture(t)
	ctx := context.Background()
	if ids := uploadDeliveryIDs(t, db, in); len(ids) != 1 || ids[0] != in.EntryID {
		t.Fatal("pending upload not listed", ids)
	}
	first := uploadDeliveryClaim(t, db, in)
	if _, err := db.ClaimSkillHubPublicationUploadDelivery(ctx, in.EntryID, in.Binding); !errors.Is(err, store.ErrSkillHubPublicationBusy) {
		t.Fatal("active upload was reclaimed", err)
	}
	if err := db.FinishSkillHubPublicationUploadDelivery(ctx, *first, "uploaded"); err != nil {
		t.Fatal(err)
	}
	if len(uploadDeliveryIDs(t, db, in)) != 0 {
		t.Fatal("uploaded retry ignored backoff")
	}
	deliveryExec(t, db, `UPDATE skillhub_publication_upload_deliveries SET next_attempt_at=clock_timestamp()-interval '1 second'`)
	second := uploadDeliveryClaim(t, db, in)
	if second.Attempt != 2 {
		t.Fatal("upload retry reset budget", second.Attempt)
	}
	if err := db.FinishSkillHubPublicationUploadDelivery(ctx, *second, "rejected"); err != nil {
		t.Fatal(err)
	}
	if len(uploadDeliveryIDs(t, db, in)) != 0 {
		t.Fatal("rejected upload retried")
	}
	if _, err := db.ClaimSkillHubPublicationUploadDelivery(ctx, in.EntryID, in.Binding); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatal("terminal upload was reported busy", err)
	}
}

func TestPostgresSkillHubPublicationUploadDeliveryRequiresCapture(t *testing.T) {
	db, _, in, _ := captureFixture(t)
	if _, err := db.ClaimSkillHubPublicationUploadDelivery(context.Background(), in.EntryID, in.Binding); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("uncaptured entry became uploadable", err)
	}
}

func uploadDeliveryMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000048_skillhub_publication_upload_deliveries." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationUploadDeliveryMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(uploadDeliveryMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationUploadDeliveriesTable), " ") {
		t.Fatal("000048 differs from startup")
	}
}
