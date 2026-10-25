package mysql

import (
	"fmt"
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
		`CREATE TEMPORARY TABLE search_group_viewer LIKE search_viewer`,
		`CREATE TEMPORARY TABLE search_sender LIKE search_viewer`,
		`CREATE TEMPORARY TABLE search_peer LIKE search_viewer`,
		`CREATE TEMPORARY TABLE topics(id varchar(64) PRIMARY KEY, type text, name text)`,
		`CREATE TEMPORARY TABLE search_group_topics LIKE topics`,
		`CREATE TEMPORARY TABLE messages(id bigint PRIMARY KEY, topic_id varchar(64), from_uid bigint, content longtext, msg_type text, created_at timestamp DEFAULT CURRENT_TIMESTAMP, content_blocks json)`,
		`CREATE TEMPORARY TABLE group_members(group_id bigint, user_id bigint)`,
		"CREATE TEMPORARY TABLE `groups`(id bigint PRIMARY KEY, name text, group_kind text)",
		`CREATE TEMPORARY TABLE bot_config(user_id bigint, owner_id bigint)`,
		`CREATE TEMPORARY TABLE friends(from_user_id bigint, to_user_id bigint, status text)`,
		`CREATE TEMPORARY TABLE conversation_titles(user_id bigint, topic_id varchar(64), title text)`,
		`INSERT INTO search_viewer VALUES (1,'human','Viewer','viewer'),(2,'bot','Agent','agent'),(3,'human','Outsider','outsider')`,
		`INSERT INTO search_group_viewer SELECT * FROM search_viewer`,
		`INSERT INTO search_sender SELECT * FROM search_viewer`,
		`INSERT INTO search_peer SELECT * FROM search_viewer`,
		`INSERT INTO topics VALUES ('grp_1','group','Visible'),('grp_2','group','Private')`,
		`INSERT INTO search_group_topics SELECT * FROM topics`,
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
	// MySQL cannot reopen a temporary table under multiple aliases/UNION arms.
	// Only fixture table names change; production predicates run verbatim.
	query := strings.NewReplacer("JOIN users viewer", "JOIN search_viewer viewer", "JOIN users sender", "JOIN search_sender sender", "JOIN users peer", "JOIN search_peer peer").Replace(mysqlMessageSearchQuery)
	query = strings.Replace(query, "JOIN search_viewer viewer", "JOIN search_group_viewer viewer", 1)
	query = strings.Replace(query, "JOIN topics t", "JOIN search_group_topics t", 1)
	searchAs := func(viewerUID int64, needle, scope string, limit int) ([]*store.MessageSearchResult, error) {
		return store.CollectMessageSearchResults(limit, func(pageSize, offset, remaining int) ([]*store.MessageSearchResult, int, error) {
			rows, err := db.db.Query(query, scope, needle, viewerUID, pageSize+offset, pageSize, offset)
			if err != nil {
				return nil, 0, err
			}
			defer rows.Close()
			return scanMySQLMessageSearch(rows, needle, scope, remaining)
		})
	}
	search := func(needle, scope string, limit int) ([]*store.MessageSearchResult, error) {
		return searchAs(1, needle, scope, limit)
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
	for _, statement := range []string{
		`INSERT INTO search_viewer VALUES (4,'bot','Owned Agent','owned'),(5,'bot','Other Agent','other'),(6,'human','Friend','friend')`,
		`INSERT INTO search_group_viewer SELECT * FROM search_viewer WHERE id >= 4`,
		`INSERT INTO search_peer SELECT * FROM search_viewer WHERE id >= 4`,
		`INSERT INTO search_sender SELECT * FROM search_viewer WHERE id >= 4`,
		`INSERT INTO bot_config VALUES (4,1),(5,3)`,
		`INSERT INTO friends VALUES (1,6,'accepted')`,
		`INSERT INTO topics VALUES ('p2p_1_4','p2p','Owned'),('p2p_1_5','p2p','Other'),('p2p_1_6','p2p','Friend'),('grp_3','group','Managed')`,
		`INSERT INTO search_group_topics VALUES ('grp_3','group','Managed')`,
		"INSERT INTO `groups` VALUES (3,'Managed','channel_managed')",
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
		results, err := searchAs(tc.uid, "accessprobe", store.MessageSearchAll, 100)
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
	t.Run("legacy filenames remain decoded and scoped", func(t *testing.T) {
		for i, content := range []string{
			`{"filename":"Old-Report.pdf"}`,
			`"{\"filename\":\"Escaped Old-Report.pdf\"}"`,
			`{"payload":{"filename":"Escaped \u004fld-Report.pdf"}}`,
			`not-json`, `"not-json"`, `null`, `{"filename":12345}`,
		} {
			if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type) VALUES (?,'grp_1',2,?,'file')`, 5000+i, content); err != nil {
				t.Fatal(err)
			}
		}
		for _, scope := range []string{store.MessageSearchAll, store.MessageSearchArtifact} {
			results, err := search("old-report", scope, 20)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, result := range results {
				ids = append(ids, result.MessageID)
			}
			if fmt.Sprint(ids) != "[5002 5001 5000]" {
				t.Fatalf("legacy scope=%s ids=%v", scope, ids)
			}
		}
	})
	t.Run("per-topic limits preserve global pages and timestamp ties", func(t *testing.T) {
		if _, err := db.db.Exec(`INSERT INTO search_sender VALUES (7,'service','Hidden','hidden')`); err != nil {
			t.Fatal(err)
		}
		var values []string
		var args []interface{}
		topics := []string{"grp_1", "p2p_1_4", "p2p_1_6"}
		for n := 1; n <= 600; n++ {
			values = append(values, "(?,?,2,'rankprobe','text',?,'[{\"type\":\"file\",\"name\":\"rankprobe.pdf\"}]')")
			args = append(args, 3000+n, topics[n%3], fmt.Sprintf("2030-01-01 00:00:%02d", (n*17)%37))
		}
		for n := 1; n <= 60; n++ {
			values = append(values, "(?,'grp_1',7,'rankprobe','text','2031-01-01','[{\"type\":\"file\",\"name\":\"rankprobe.pdf\"}]')")
			args = append(args, 4000+n)
		}
		if _, err := db.db.Exec(`INSERT INTO messages (id,topic_id,from_uid,content,msg_type,created_at,content_blocks) VALUES `+strings.Join(values, ","), args...); err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{store.MessageSearchAll, store.MessageSearchMessage, store.MessageSearchArtifact} {
			for _, offset := range []int{0, 7, 97, 597, 607} {
				wantRows, err := db.db.Query(`SELECT m.id FROM messages m JOIN search_sender sender ON sender.id=m.from_uid
 WHERE m.content='rankprobe' AND sender.account_type IN ('human','bot')
 ORDER BY m.created_at DESC,m.id DESC LIMIT 7 OFFSET ?`, offset)
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
				rows, err := db.db.Query(query, scope, "rankprobe", 1, 7+offset, 7, offset)
				if err != nil {
					t.Fatal(err)
				}
				page, _, err := scanMySQLMessageSearch(rows, "rankprobe", scope, 7)
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
}
