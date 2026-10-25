package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func TestPostgresMessageSearchVisibleText(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for database integration")
	}
	db := messageSearchSchemaTestAdapter(t, dsn)
	db.db.SetMaxOpenConns(1)
	// Private-schema tables exercise the production query and expression index.
	for _, statement := range []string{
		`CREATE TEMP TABLE users(id bigint PRIMARY KEY, account_type text, display_name text, username text)`,
		`CREATE TEMP TABLE topics(id text PRIMARY KEY, type text, name text)`,
		`CREATE TEMP TABLE messages(id bigint PRIMARY KEY, topic_id text, from_uid bigint, content text, msg_type text, created_at timestamptz DEFAULT now(), content_blocks jsonb)`,
		`CREATE INDEX ON messages(topic_id,created_at)`,
		createMessageSearchDocument,
		createMessageSearchIndex,
		`CREATE TEMP TABLE group_members(group_id bigint, user_id bigint)`,
		`CREATE TEMP TABLE "groups"(id bigint PRIMARY KEY, name text, group_kind text)`,
		`CREATE TEMP TABLE bot_config(user_id bigint, owner_id bigint)`,
		`CREATE TEMP TABLE friends(from_user_id bigint, to_user_id bigint, status text)`,
		`CREATE TEMP TABLE conversation_titles(user_id bigint, topic_id text, title text)`,
		`INSERT INTO users VALUES (1,'human','Viewer','viewer'),(2,'bot','Agent','agent'),(3,'human','Outsider','outsider')`,
		`INSERT INTO topics VALUES ('grp_1','group','Visible'),('grp_2','group','Private')`,
		`INSERT INTO group_members VALUES(1,1),(2,3)`,
		`INSERT INTO "groups" VALUES(1,'Visible','standard'),(2,'Private','standard')`,
	} {
		statement = strings.Replace(statement, "CREATE TEMP TABLE", "CREATE TABLE", 1)
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	fixtures := []struct{ content, blocks string }{
		{"部署成功", `null`},
		{"", `[{"type":"text","text":"部署成功 UPPERCASE"}]`},
		{"private legacy body", `[{"type":"thinking","thinking":"private reasoning"},{"type":"assistant_text","text":"部署成功"}]`},
		{"private legacy body", `[{"type":"tool_result","content":"private output"},{"type":"text","content":"部署成功"}]`},
		{"private legacy body", `[{"type":"text","text":"第一段"},{"type":"assistant_text","text":"部署成功"}]`},
		{"", `[{"type":"text","text":"` + strings.Repeat("前", 2200) + `部署成功"}]`},
		{"部署成功", `[{"type":"text","text":""}]`},
		{"部署成功", `[{"type":"text","text":"unrelated visible answer"}]`},
		{"部署成功", `[{"type":"debug","content":"部署成功"}]`},
		{"部署成功", `[{"type":"thinking","thinking":"部署成功"}]`},
		{"部署成功", `{"type":"text"}`},
		{"部署成功", `[{"type":"text","text":" ","content":"部署成功"}]`},
	}
	for i, fixture := range fixtures {
		if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,content_blocks) VALUES ($1,'grp_1',2,$2,'text',$3::jsonb)`, i+1, fixture.content, fixture.blocks); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type) VALUES (20,'grp_2',3,'部署成功','text')`); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{store.MessageSearchAll, store.MessageSearchMessage} {
		t.Run(scope, func(t *testing.T) {
			results, err := db.SearchMessages(context.Background(), 1, "部署", scope, 100)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, r := range results {
				ids = append(ids, r.MessageID)
				if strings.Contains(r.Content, "private") || strings.Contains(r.Snippet, "private") || !strings.Contains(r.Snippet, "部署") {
					t.Fatalf("unsafe or missing excerpt: %+v", r)
				}
			}
			if fmt.Sprint(ids) != "[7 6 5 4 3 2 1]" {
				t.Fatalf("visible result IDs=%v", ids)
			}
		})
	}
	for _, query := range []string{"private", "reasoning", "output"} {
		results, err := db.SearchMessages(context.Background(), 1, query, store.MessageSearchAll, 100)
		if err != nil || len(results) != 0 {
			t.Fatalf("private query %q: results=%+v err=%v", query, results, err)
		}
	}
	results, err := db.SearchMessages(context.Background(), 1, "uppercase", store.MessageSearchMessage, 100)
	if err != nil || len(results) != 1 || results[0].MessageID != 2 {
		t.Fatalf("case-insensitive block search results=%+v err=%v", results, err)
	}
	results, err = db.SearchMessages(context.Background(), 1, "部署", store.MessageSearchArtifact, 100)
	if err != nil || len(results) != 0 {
		t.Fatalf("artifact scope results=%+v err=%v", results, err)
	}
	if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,content_blocks)
 VALUES (30,'grp_1',2,'private legacy body','text','[{"type":"tool_result","content":"private output"},{"type":"file","name":"Visible Report.pdf"}]'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{store.MessageSearchAll, store.MessageSearchArtifact} {
		results, err := db.SearchMessages(context.Background(), 1, "report", scope, 100)
		if err != nil || len(results) != 1 || results[0].Content != "Visible Report.pdf" || results[0].Snippet != "Visible Report.pdf" {
			t.Fatalf("artifact response results=%+v err=%v", results, err)
		}
	}
	// False matches in a stale legacy body must not consume the 2,000-row budget.
	if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,content_blocks)
 SELECT 100+n,'grp_1',2,'部署成功','text','[{"type":"text","text":"unrelated visible answer"}]'::jsonb FROM generate_series(1,2100) AS n`); err != nil {
		t.Fatal(err)
	}
	results, err = db.SearchMessages(context.Background(), 1, "部署", store.MessageSearchMessage, 20)
	if err != nil || len(results) != 7 {
		t.Fatalf("valid older hits were crowded out: count=%d err=%v", len(results), err)
	}
	// Both UNION arms must retain the existing group and private-Agent policy.
	for _, statement := range []string{
		`INSERT INTO users VALUES (4,'bot','Owned Agent','owned'),(5,'bot','Other Agent','other'),(6,'human','Friend','friend')`,
		`INSERT INTO bot_config VALUES (4,1),(5,3)`,
		`INSERT INTO friends VALUES (1,6,'accepted')`,
		`INSERT INTO topics VALUES ('p2p_1_4','p2p','Owned'),('p2p_1_5','p2p','Other'),('p2p_1_6','p2p','Friend'),('grp_3','group','Managed')`,
		`INSERT INTO "groups" VALUES (3,'Managed','channel_managed')`,
		`INSERT INTO group_members VALUES (3,1),(3,4)`,
		`INSERT INTO conversation_titles VALUES (1,'p2p_1_4','My task')`,
		`INSERT INTO messages (id,topic_id,from_uid,content,msg_type) VALUES (40,'p2p_1_4',4,'accessprobe','text'),(41,'p2p_1_5',5,'accessprobe','text'),(42,'p2p_1_6',6,'accessprobe','text'),(43,'grp_3',4,'accessprobe','text')`,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		uid int64
		ids string
	}{{1, "[42 40]"}, {4, "[43 40]"}, {5, "[41]"}, {3, "[]"}} {
		results, err := db.SearchMessages(context.Background(), tc.uid, "accessprobe", store.MessageSearchAll, 100)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, result := range results {
			ids = append(ids, result.MessageID)
			if tc.uid == 1 && result.MessageID == 40 && result.TopicName != "My task" {
				t.Fatalf("custom title lost: %+v", result)
			}
		}
		if fmt.Sprint(ids) != tc.ids {
			t.Fatalf("viewer %d: ids=%v want %s", tc.uid, ids, tc.ids)
		}
	}
	t.Run("per-topic limits preserve global pages and timestamp ties", func(t *testing.T) {
		if _, err := db.db.Exec(`INSERT INTO users VALUES (7,'service','Hidden','hidden')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,created_at,content_blocks)
 SELECT 3000+n, CASE n%3 WHEN 0 THEN 'grp_1' WHEN 1 THEN 'p2p_1_4' ELSE 'p2p_1_6' END,
 2,'rankprobe','text',TIMESTAMPTZ '2030-01-01' + ((n*17)%37)*INTERVAL '1 second',
 '[{"type":"file","name":"rankprobe.pdf"}]'::jsonb FROM generate_series(1,600)n
 UNION ALL
 SELECT 4000+n,'grp_1',7,'rankprobe','text',TIMESTAMPTZ '2031-01-01',
 '[{"type":"file","name":"rankprobe.pdf"}]'::jsonb FROM generate_series(1,60)n`); err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{store.MessageSearchAll, store.MessageSearchMessage, store.MessageSearchArtifact} {
			for _, offset := range []int{0, 7, 97, 597, 607} {
				wantRows, err := db.db.Query(`SELECT m.id FROM messages m JOIN users sender ON sender.id=m.from_uid
 WHERE m.content='rankprobe' AND sender.account_type IN ('human','bot')
 ORDER BY m.created_at DESC,m.id DESC LIMIT 7 OFFSET $1`, offset)
				if err != nil {
					t.Fatal(err)
				}
				var want []int64
				for wantRows.Next() {
					var id int64
					if err := wantRows.Scan(&id); err != nil {
						t.Fatal(err)
					}
					want = append(want, id)
				}
				if err := wantRows.Err(); err != nil {
					t.Fatal(err)
				}
				wantRows.Close()
				rows, err := db.db.Query(postgresMessageSearchQuery, 1, scope, "rankprobe", 7, offset)
				if err != nil {
					t.Fatal(err)
				}
				page, _, err := scanPostgresMessageSearch(rows, "rankprobe", scope, 7)
				rows.Close()
				if err != nil {
					t.Fatal(err)
				}
				var got []int64
				for _, result := range page {
					got = append(got, result.MessageID)
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("scope=%s offset=%d: ids=%v want %v", scope, offset, got, want)
				}
			}
		}
	})
	// Scale the user/topic relation independently of the number of actual hits.
	// The former computed LEFT JOIN compared every topic against every user.
	for _, statement := range []string{
		`INSERT INTO users SELECT n,'human','Peer','peer_'||n FROM generate_series(100,50099)n`,
		`INSERT INTO topics SELECT 'p2p_1_'||n,'p2p','History' FROM generate_series(100,2099)n`,
		`ANALYZE users`, `ANALYZE topics`, `ANALYZE messages`,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results, err = db.SearchMessages(ctx, 1, "部署", store.MessageSearchMessage, 20)
	if err != nil || len(results) != 7 {
		t.Fatalf("search with 50,000 users and 2,000 empty histories: count=%d err=%v", len(results), err)
	}
	t.Run("indexed selective candidates and literal patterns", func(t *testing.T) {
		testPostgresMessageSearchIndexedCandidates(t, db)
	})
}
