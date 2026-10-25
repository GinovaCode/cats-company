package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgresMessageSearchGramMigration(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for built-in GIN migration")
	}
	db := messageSearchSchemaTestAdapter(t, dsn)
	for _, statement := range []string{
		`CREATE TABLE messages(id bigint PRIMARY KEY,msg_type text,content text,content_blocks jsonb)`,
		`INSERT INTO messages VALUES(1,'text','historical course2md',NULL),(2,'text','another historical row',NULL)`,
		createMessageSearchDocument, createMessageSearchGrams, createMessageSearchGramIndex,
		createMessageSearchGrams, createMessageSearchGramIndex,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate an interrupted concurrent build and exercise the same repair
	// helper used by startup after extension installation is rejected.
	if _, err := db.db.Exec(`DROP INDEX CONCURRENTLY idx_messages_search_grams_gin`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY idx_messages_search_grams_gin ON messages((true))`); err == nil {
		t.Fatal("expected an invalid concurrent index")
	}
	ctx := context.Background()
	conn, err := db.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ensureMessageSearchIndexDDL(ctx, conn, "idx_messages_search_grams_gin", createMessageSearchGramIndex); err != nil {
			conn.Close()
			t.Fatal(err)
		}
	}
	conn.Close()
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM messages WHERE message_search_grams_v1(message_search_document_v1(msg_type,content,content_blocks)) @> message_search_grams_v1('course2md')`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("historical index candidates: count=%d err=%v", count, err)
	}
	for _, name := range []string{"000059_message_search_gram_index.down.sql", "000058_message_search_grams.down.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "migrations", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rollback changed history: count=%d err=%v", count, err)
	}
}

func TestPostgresMessageSearchGramContainment(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for UTF-8 gram contract")
	}
	db := messageSearchSchemaTestAdapter(t, dsn)
	if _, err := db.db.Exec(createMessageSearchGrams); err != nil {
		t.Fatal(err)
	}
	// Check every rune-aligned substring, including repetitions, mixed byte
	// widths, emoji, newlines, punctuation, SQL LIKE metacharacters and case.
	for _, document := range []string{"AaaAaBbb", "多语言部署🙂测试éZ", "100%_ready!\\line\nbreak"} {
		runes := []rune(document)
		for start := range runes {
			for end := start + 1; end <= len(runes); end++ {
				needle := string(runes[start:end])
				var match bool
				if err := db.db.QueryRow(`SELECT message_search_grams_v1(lower($1)) @> message_search_grams_v1(lower($2))`, document, needle).Scan(&match); err != nil || !match {
					t.Fatalf("omitted substring %q in %q: match=%v err=%v", needle, document, match, err)
				}
			}
		}
	}
	var keyCount int
	if err := db.db.QueryRow(`SELECT cardinality(message_search_grams_v1(repeat('a',10000)))`).Scan(&keyCount); err != nil || keyCount != 1 {
		t.Fatalf("repeated text keys were not deduplicated: count=%d err=%v", keyCount, err)
	}
}
