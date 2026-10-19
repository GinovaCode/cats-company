package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestSessionForkPostgresMigrationMatchesStartup(t *testing.T) {
	raw, err := os.ReadFile("../migrations/postgres/000041_session_forks.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != strings.TrimSpace(migrateSessionForks) {
		t.Fatal("session fork startup migration differs from numbered migration")
	}
}
