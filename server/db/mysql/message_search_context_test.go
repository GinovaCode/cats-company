package mysql

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
	mock.ExpectQuery("WITH search_params").
		WithArgs("all", "course2md", int64(1), 200, 0).
		WillDelayFor(time.Second).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = (&Adapter{db: db}).SearchMessages(ctx, 1, "course2md", "all", 20)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("database query ignored context cancellation: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
