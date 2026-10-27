package mysql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestMessageSearchBoundsClientWait(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("WITH search_params").
		WithArgs("all", "course2md", int64(1), 200, 200, 0).
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

func TestMessageSearchClassifiesDatabaseTimeout(t *testing.T) {
	for _, duringRows := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		serverErr := &mysqldriver.MySQLError{Number: 3024, Message: "maximum statement execution time exceeded"}
		expected := mock.ExpectQuery("WITH search_params").WithArgs("all", "course2md", int64(1), 200, 200, 0)
		if duringRows {
			expected.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).RowError(0, serverErr))
		} else {
			expected.WillReturnError(serverErr)
		}
		_, err = (&Adapter{db: db}).SearchMessages(context.Background(), 1, "course2md", "all", 20)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("duringRows=%v: error=%v", duringRows, err)
		}
		var mysqlErr *mysqldriver.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 3024 {
			t.Fatal("original database error was lost")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}
