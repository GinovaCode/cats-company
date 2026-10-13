package server

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestUsersWithAccountTypePreservesPublicFieldsAndClassifiesSubjects(t *testing.T) {
	users := usersWithAccountType([]*types.User{
		{ID: 7, Username: "alice", DisplayName: "Alice", AccountType: types.AccountHuman},
		{ID: 44, Username: "alice-bot", DisplayName: "Alice Bot", AccountType: types.AccountBot, BotDisclose: true},
	})
	if len(users) != 2 {
		t.Fatalf("users=%d, want 2", len(users))
	}
	if users[0]["account_type"] != types.AccountHuman || users[1]["account_type"] != types.AccountBot {
		t.Fatalf("unexpected account types: %+v", users)
	}
	if users[0]["username"] != "alice" || users[1]["display_name"] != "Alice Bot" {
		t.Fatalf("public identity fields were not preserved: %+v", users)
	}
	if users[1]["bot"] != true {
		t.Fatalf("bot disclosure flag was not preserved: %+v", users[1])
	}
}

func TestUsersWithAccountTypeSkipsNilEntries(t *testing.T) {
	users := usersWithAccountType([]*types.User{nil, {ID: 7, AccountType: types.AccountHuman}})
	if len(users) != 1 || users[0]["id"] != int64(7) {
		t.Fatalf("unexpected nil filtering: %+v", users)
	}
}
