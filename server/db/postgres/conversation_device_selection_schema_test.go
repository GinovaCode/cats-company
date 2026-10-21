package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestDeviceSelectionStartupSchemaMatchesMigration(t *testing.T) {
	migration, err := os.ReadFile("../migrations/postgres/000041_conversation_device_selections.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(migration)) != strings.TrimSpace(createConversationDeviceSelectionsTable) {
		t.Fatal("device selection startup and numbered migration differ")
	}
	if !strings.Contains(createConversationDeviceSelectionsTable, "ON DELETE CASCADE") || strings.Contains(createConversationDeviceSelectionsTable, "REFERENCES topics") {
		t.Fatal("selection must follow account lifecycle without requiring an existing topic row")
	}
}
