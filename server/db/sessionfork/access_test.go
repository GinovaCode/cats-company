package sessionfork

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store"
)

func TestValidateSourceOwnerOrFriend(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		for _, tc := range []struct {
			name, topic string
			owner       int64
			friend      bool
			members     []int64
			allowed     bool
		}{
			{"owner", "p2p_7_9", 7, false, nil, true},
			{"friend", "p2p_7_9", 8, true, nil, true},
			{"revoked", "p2p_7_9", 8, false, nil, false},
			{"foreign direct", "p2p_8_9", 8, true, nil, false},
			{"friend group", "grp_20", 8, true, []int64{7, 9}, true},
			{"foreign group", "grp_20", 8, true, []int64{8, 9}, false},
			{"multi human", "grp_20", 8, true, []int64{7, 8, 9}, false},
			{"multi bot", "grp_20", 8, true, []int64{7, 9, 10}, false},
		} {
			t.Run(tc.name+map[bool]string{false: "/mysql", true: "/postgres"}[postgres], func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				s := SQLStore{DB: db, Postgres: postgres}
				mock.ExpectBegin()
				mock.ExpectQuery(regexp.QuoteMeta(s.query("SELECT b.owner_id,b.body_id,u.account_type,u.state FROM bot_config b JOIN users u ON u.id=b.user_id WHERE b.user_id = ?"))).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner", "body", "type", "state"}).AddRow(tc.owner, "body", "bot", 0))
				if tc.owner != 7 {
					count := 0
					if tc.friend {
						count = 1
					}
					mock.ExpectQuery(regexp.QuoteMeta(s.query("SELECT COUNT(*) FROM friends WHERE from_user_id = ? AND to_user_id = ? AND status = 'accepted'"))).WithArgs(int64(7), int64(9)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
				}
				if tc.members != nil {
					rows := sqlmock.NewRows([]string{"user_id"})
					for _, id := range tc.members {
						rows.AddRow(id)
					}
					mock.ExpectQuery(regexp.QuoteMeta(s.query("SELECT user_id FROM group_members WHERE group_id = ? ORDER BY user_id"))).WithArgs(int64(20)).WillReturnRows(rows)
				}
				mock.ExpectRollback()
				tx, err := db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				err = s.validateSource(context.Background(), tx, 7, 9, tc.topic, "body")
				if tc.allowed && err != nil {
					t.Fatal(err)
				}
				if !tc.allowed && !errors.Is(err, store.ErrSessionForkForbidden) {
					t.Fatalf("expected forbidden, got %v", err)
				}
				tx.Rollback()
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
