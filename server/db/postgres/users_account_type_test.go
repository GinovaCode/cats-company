package postgres

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresSearchUsersPreservesAccountType(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	mock.ExpectQuery(`SELECT u\.id, u\.username, u\.display_name, COALESCE\(u\.avatar_url, ''\), u\.account_type, COALESCE\(u\.bot_disclose, false\) FROM users u`).
		WithArgs("%alice%", "%alice%", "alice", 20).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "display_name", "avatar_url", "account_type", "bot_disclose"}).
			AddRow(int64(7), "alice", "Alice", "", "human", false).
			AddRow(int64(44), "alice-bot", "Alice Bot", "", "bot", true))

	users, err := adapter.SearchUsers("alice", 20)
	if err != nil {
		t.Fatalf("search users: %v", err)
	}
	if len(users) != 2 || users[0].AccountType != types.AccountHuman || users[1].AccountType != types.AccountBot {
		t.Fatalf("unexpected account types: %+v", users)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}
