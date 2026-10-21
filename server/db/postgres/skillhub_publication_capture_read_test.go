package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func TestPostgresSkillHubPublicationCaptureReadAuthority(t *testing.T) {
	db, _, in, other := captureFixture(t)
	ctx := context.Background()
	if r, err := db.ReadSkillHubPublicationCapture(ctx, in.EntryID, in.Binding); err != nil || r != nil {
		t.Fatal("unrecorded capture", r, err)
	}
	first := recordCaptureForTest(t, db, in)
	if r, err := db.ReadSkillHubPublicationCapture(ctx, in.EntryID, in.Binding); err != nil || r == nil || *r != *first {
		t.Fatal("capture read", r, err)
	}
	bad := in.Binding
	bad.ConnectionID = "wrong-socket"
	if _, err := db.ReadSkillHubPublicationCapture(ctx, in.EntryID, bad); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("socket substitution", err)
	}
	if _, err := db.BumpBotConnectionGeneration(in.Binding.BotUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadSkillHubPublicationCapture(ctx, in.EntryID, in.Binding); !errors.Is(err, store.ErrSkillHubPublicationStale) {
		t.Fatal("stale generation", err)
	}
	in.Binding.Source.RouteGeneration++
	in.Binding.NodeID = "new-node"
	in.Binding.ConnectionID = "new-socket"
	if r, err := db.ReadSkillHubPublicationCapture(ctx, in.EntryID, in.Binding); err != nil || r == nil || *r != *first {
		t.Fatal("reconnect rewrote evidence", r, err)
	}
	deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
	if _, err := db.ReadSkillHubPublicationCapture(ctx, in.EntryID, in.Binding); err == nil {
		t.Fatal("old owner retained live authority")
	}
	if r, err := db.GetSkillHubPublicationCaptureStatus(ctx, in.Binding.ActorUID, in.EntryID); err != nil || r == nil || r.State != "captured" {
		t.Fatal("history lost", r, err)
	}
	if r, err := db.GetSkillHubPublicationCaptureStatus(ctx, other, in.EntryID); err != nil || r != nil {
		t.Fatal("new owner read old history", r, err)
	}
}
func TestPostgresSkillHubPublicationCaptureStatusEvidenceAndCancellation(t *testing.T) {
	db, _, in, _ := captureFixture(t)
	ctx := context.Background()
	read := func(want string) {
		t.Helper()
		s, err := db.GetSkillHubPublicationCaptureStatus(ctx, in.Binding.ActorUID, in.EntryID)
		if err != nil || s == nil || s.State != want || s.ObservedAt.IsZero() {
			t.Fatalf("want %s: %+v %v", want, s, err)
		}
	}
	read("waiting_runtime")
	l := captureDeliveryClaim(t, db, in)
	read("leased")
	if err := db.FinishSkillHubPublicationCaptureDelivery(ctx, *l, "retry"); err != nil {
		t.Fatal(err)
	}
	read("pending")
	recordCaptureForTest(t, db, in)
	// Crash between durable evidence and scheduler settlement must still read captured.
	read("captured")
	if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
		t.Fatal(err)
	}
	read("cancelled")
	if _, err := db.ReadSkillHubPublicationCapture(ctx, in.EntryID, in.Binding); err == nil {
		t.Fatal("cancelled live read")
	}
	deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
	if s, err := db.GetSkillHubPublicationCaptureStatus(ctx, in.Binding.ActorUID, in.EntryID); err != nil || s != nil {
		t.Fatal("inactive human history", s, err)
	}
}
