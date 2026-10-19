package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSessionForkPostgresMigrationMatchesStartup(t *testing.T) {
	raw, err := os.ReadFile("../migrations/postgres/000041_session_forks.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, eol := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("EOL=%q", eol), func(t *testing.T) {
			migration := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", eol)
			if strings.TrimSpace(strings.ReplaceAll(migration, "\r\n", "\n")) != strings.TrimSpace(strings.ReplaceAll(migrateSessionForks, "\r\n", "\n")) {
				t.Fatal("session fork startup migration differs from numbered migration")
			}
		})
	}
}
