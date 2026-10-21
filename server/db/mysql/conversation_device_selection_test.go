package mysql

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
	"regexp"
	"testing"
)

func TestConversationDeviceSelectionCAS(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &Adapter{db: db}
	ctx := context.Background()
	const session = "session:v2:catscompany:p2p:p2p_7_42:agent:usr42"
	get := regexp.QuoteMeta(`SELECT device_id, revision, source, updated_at_ms FROM conversation_device_selections WHERE actor_uid = ? AND session_key = ?`)
	insert := regexp.QuoteMeta(`INSERT INTO conversation_device_selections (actor_uid, session_key, device_id, revision, source, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?)`)
	update := regexp.QuoteMeta(`UPDATE conversation_device_selections SET device_id = ?, revision = ?, source = ?, updated_at_ms = ? WHERE actor_uid = ? AND session_key = ? AND revision = ?`)
	mock.ExpectQuery(get).WithArgs(int64(7), session).WillReturnRows(sqlmock.NewRows([]string{"device_id", "revision", "source", "updated_at_ms"}))
	if record, err := a.GetConversationDeviceSelection(ctx, 7, session); err != nil || record != nil {
		t.Fatal(record, err)
	}
	next := store.ConversationDeviceSelection{DeviceID: "a", Revision: "revision-a", Source: "user_confirmation", UpdatedAt: 1}
	mock.ExpectExec(insert).WithArgs(int64(7), session, "a", "revision-a", "user_confirmation", int64(1)).WillReturnResult(sqlmock.NewResult(1, 1))
	if ok, err := a.CompareConversationDeviceSelection(ctx, 7, session, "", next); err != nil || !ok {
		t.Fatal(ok, err)
	}
	mock.ExpectExec(insert).WithArgs(int64(7), session, "a", "revision-a", "user_confirmation", int64(1)).WillReturnError(&driver.MySQLError{Number: 1062, Message: "duplicate"})
	if ok, err := a.CompareConversationDeviceSelection(ctx, 7, session, "", next); err != nil || ok {
		t.Fatal("duplicate insert won", ok, err)
	}
	mock.ExpectQuery(get).WithArgs(int64(7), session).WillReturnRows(sqlmock.NewRows([]string{"device_id", "revision", "source", "updated_at_ms"}).AddRow("a", "revision-a", "user_confirmation", 1))
	if record, err := a.GetConversationDeviceSelection(ctx, 7, session); err != nil || record == nil || record.DeviceID != "a" {
		t.Fatal(record, err)
	}
	next.DeviceID = "b"
	next.Revision = "revision-b"
	for _, rows := range []int64{1, 0} {
		mock.ExpectExec(update).WithArgs("b", "revision-b", "user_confirmation", int64(1), int64(7), session, "revision-a").WillReturnResult(sqlmock.NewResult(0, rows))
		if ok, err := a.CompareConversationDeviceSelection(ctx, 7, session, "revision-a", next); err != nil || ok != (rows == 1) {
			t.Fatal("CAS", ok, err)
		}
	}
	mock.ExpectQuery(get).WithArgs(int64(7), session).WillReturnError(errors.New("db down"))
	if _, err := a.GetConversationDeviceSelection(ctx, 7, session); err == nil {
		t.Fatal("read error hidden")
	}
	mock.ExpectExec(update).WithArgs("b", "revision-b", "user_confirmation", int64(1), int64(7), session, "revision-a").WillReturnError(errors.New("db down"))
	if ok, err := a.CompareConversationDeviceSelection(ctx, 7, session, "revision-a", next); err == nil || ok {
		t.Fatal("write error hidden")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
