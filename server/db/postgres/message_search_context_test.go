package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMessageSearchCancelsDatabaseWork(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(postgresMessageSearchPlanMode).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("WITH readable_topics").
		WithArgs(int64(1), "all", "course2md", 200, 0).
		WillDelayFor(time.Second).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = (&Adapter{db: db}).SearchMessages(ctx, 1, "course2md", "all", 20)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("database query ignored context cancellation: %v", err)
	}
	// database/sql can roll back an expired transaction asynchronously.
	deadline := time.Now().Add(time.Second)
	for {
		err := mock.ExpectationsWereMet()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}
