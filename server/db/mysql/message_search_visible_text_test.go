package mysql

import (
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func TestMySQLMessageSearchVisibleText(t *testing.T) {
	dsn := os.Getenv("CATS_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN for database integration")
	}
	db := &Adapter{}
	if err := db.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TEMPORARY TABLE search_viewer(id bigint PRIMARY KEY, account_type text, display_name text, username text)`,
		`CREATE TEMPORARY TABLE search_sender LIKE search_viewer`,
		`CREATE TEMPORARY TABLE search_peer LIKE search_viewer`,
		`CREATE TEMPORARY TABLE topics(id varchar(64) PRIMARY KEY, type text, name text)`,
		`CREATE TEMPORARY TABLE messages(id bigint PRIMARY KEY, topic_id varchar(64), from_uid bigint, content longtext, msg_type text, created_at timestamp DEFAULT CURRENT_TIMESTAMP, content_blocks json)`,
		`CREATE TEMPORARY TABLE group_members(group_id bigint, user_id bigint)`,
		"CREATE TEMPORARY TABLE `groups`(id bigint PRIMARY KEY, name text, group_kind text)",
		`CREATE TEMPORARY TABLE bot_config(user_id bigint, owner_id bigint)`,
		`CREATE TEMPORARY TABLE friends(from_user_id bigint, to_user_id bigint, status text)`,
		`CREATE TEMPORARY TABLE conversation_titles(user_id bigint, topic_id varchar(64), title text)`,
		`INSERT INTO search_viewer VALUES (1,'human','Viewer','viewer'),(2,'bot','Agent','agent'),(3,'human','Outsider','outsider')`,
		`INSERT INTO search_sender SELECT * FROM search_viewer`,
		`INSERT INTO search_peer SELECT * FROM search_viewer`,
		`INSERT INTO topics VALUES ('grp_1','group','Visible'),('grp_2','group','Private')`,
		`INSERT INTO group_members VALUES(1,1),(2,3)`,
		"INSERT INTO `groups` VALUES(1,'Visible','standard'),(2,'Private','standard')",
	} {
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
		if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,content_blocks) VALUES (?,'grp_1',2,?,'text',?)`, i+1, fixture.content, fixture.blocks); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type) VALUES (20,'grp_2',3,'部署成功','text')`); err != nil {
		t.Fatal(err)
	}
	// MySQL cannot reopen a temporary table under multiple aliases. Only the three
	// user-table names change; the production search predicates run verbatim.
	query := strings.NewReplacer("JOIN users viewer", "JOIN search_viewer viewer", "JOIN users sender", "JOIN search_sender sender", "JOIN users peer", "JOIN search_peer peer").Replace(mysqlMessageSearchQuery)
	search := func(needle, scope string, limit int) ([]*store.MessageSearchResult, error) {
		return store.CollectMessageSearchResults(limit, func(pageSize, offset, remaining int) ([]*store.MessageSearchResult, int, error) {
			rows, err := db.db.Query(query, scope, needle, 1, pageSize, offset)
			if err != nil {
				return nil, 0, err
			}
			defer rows.Close()
			return scanMySQLMessageSearch(rows, needle, scope, remaining)
		})
	}
	for _, scope := range []string{store.MessageSearchAll, store.MessageSearchMessage} {
		t.Run(scope, func(t *testing.T) {
			results, err := search("部署", scope, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 7 {
				t.Fatalf("visible result count=%d, want 7", len(results))
			}
			for i, r := range results {
				if r.MessageID != int64(7-i) {
					t.Fatalf("result %d: id=%d", i, r.MessageID)
				}
				if strings.Contains(r.Content, "private") || strings.Contains(r.Snippet, "private") || !strings.Contains(r.Snippet, "部署") {
					t.Fatalf("unsafe or missing excerpt: %+v", r)
				}
			}
		})
	}
	for _, needle := range []string{"private", "reasoning", "output"} {
		results, err := search(needle, store.MessageSearchAll, 100)
		if err != nil || len(results) != 0 {
			t.Fatalf("private query %q: results=%+v err=%v", needle, results, err)
		}
	}
	results, err := search("uppercase", store.MessageSearchMessage, 100)
	if err != nil || len(results) != 1 || results[0].MessageID != 2 {
		t.Fatalf("case-insensitive block search results=%+v err=%v", results, err)
	}
	results, err = search("部署", store.MessageSearchArtifact, 100)
	if err != nil || len(results) != 0 {
		t.Fatalf("artifact scope results=%+v err=%v", results, err)
	}
	if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,content_blocks)
 VALUES (30,'grp_1',2,'private legacy body','text',?)`, `[{"type":"tool_result","content":"private output"},{"type":"file","name":"Visible Report.pdf"}]`); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{store.MessageSearchAll, store.MessageSearchArtifact} {
		results, err := search("report", scope, 100)
		if err != nil || len(results) != 1 || results[0].Content != "Visible Report.pdf" || results[0].Snippet != "Visible Report.pdf" {
			t.Fatalf("artifact response results=%+v err=%v", results, err)
		}
	}
	var placeholders []string
	var args []interface{}
	for i := 0; i < 2100; i++ {
		placeholders = append(placeholders, "(?,'grp_1',2,'部署成功','text',?)")
		args = append(args, 100+i, `[{"type":"text","text":"unrelated visible answer"}]`)
	}
	if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,content_blocks) VALUES `+strings.Join(placeholders, ","), args...); err != nil {
		t.Fatal(err)
	}
	results, err = search("部署", store.MessageSearchMessage, 20)
	if err != nil || len(results) != 7 {
		t.Fatalf("valid older hits were crowded out: count=%d err=%v", len(results), err)
	}
}
