package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type humanCommitDB struct {
	*publicationReadTestStore
	mu        sync.Mutex
	receipt   store.SkillHubPublicationCloudReceipt
	intent    *store.SkillHubPublicationCommitIntent
	denied    bool
	recordErr error
	records   int
}

func (s *humanCommitDB) GetSkillHubPublicationCommitContext(ctx context.Context, actor int64, id string, current bool) (*store.SkillHubPublicationCommitContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.denied || actor != s.entry.Input.ActorUID || id != s.entry.ID {
		return nil, nil
	}
	if current && s.entry.State == "cancelled" {
		return nil, store.ErrSkillHubPublicationSettled
	}
	r := s.receipt
	return &store.SkillHubPublicationCommitContext{Entry: *s.entry, Receipt: &r, Intent: s.intent}, nil
}
func (s *humanCommitDB) RecordSkillHubPublicationCommitIntent(ctx context.Context, actor int64, in store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return nil, s.recordErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.denied || actor != s.entry.Input.ActorUID {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if s.entry.State == "cancelled" {
		return nil, store.ErrSkillHubPublicationSettled
	}
	if s.intent != nil {
		if s.intent.Input != in {
			return nil, store.ErrSkillHubPublicationConflict
		}
		return s.intent, nil
	}
	hash, _ := in.IdentityHash()
	s.records++
	s.intent = &store.SkillHubPublicationCommitIntent{Input: in, IdentityHash: hash, RequestedAt: time.Now()}
	return s.intent, nil
}
func (s *humanCommitDB) AuthorizeSkillHubPublicationCommit(context.Context, store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitPermit, error) {
	panic("human route must not issue permits")
}
func (s *humanCommitDB) GetSkillHubPublicationCommitPermit(context.Context, int64, string) (*store.SkillHubPublicationCommitPermit, error) {
	panic("human route must not expose permits")
}

type humanCommitCloud struct {
	in        store.SkillHubPublicationCommitInput
	plan      func(context.Context) error
	read      func(context.Context) error
	authorize func(context.Context) error
	state     string
	result    *PublicationCommitCloudResult
}

func (c *humanCommitCloud) Plan(ctx context.Context, r store.SkillHubPublicationCloudReceipt, b bool) (*store.SkillHubPublicationCommitInput, error) {
	if c.plan != nil {
		if e := c.plan(ctx); e != nil {
			return nil, e
		}
	}
	in := c.in
	in.Target.ConfirmVersionPublish = b
	return &in, nil
}
func (c *humanCommitCloud) Read(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
	if c.read != nil {
		if e := c.read(ctx); e != nil {
			return nil, e
		}
	}
	out := &PublicationCommitCloudStatus{SchemaVersion: 1, Receipt: publicationControlReceipt(c.in.Receipt), State: c.state, Target: &c.in.Target, Result: c.result}
	if publicationCloudCommitState(c.state) {
		p := "pubpermit_" + strings.Repeat("a", 32)
		out.PermitID = &p
	}
	return out, nil
}
func (c *humanCommitCloud) Authorize(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
	if c.authorize != nil {
		if e := c.authorize(ctx); e != nil {
			return nil, e
		}
	}
	permit := "pubpermit_" + strings.Repeat("a", 32)
	return &PublicationCommitCloudStatus{SchemaVersion: 1, Receipt: publicationControlReceipt(r), State: "committing", PermitID: &permit}, nil
}
func humanCommitFixture() (*humanCommitDB, *humanCommitCloud, *SkillHubPublicationHumanCommitHandler) {
	db := &humanCommitDB{publicationReadTestStore: newPublicationReadTestStore(), receipt: cancellationTestLease("https://hub.example").Receipt}
	db.receipt.Query.EntryID = publicationReadTestID
	c := &humanCommitCloud{in: store.SkillHubPublicationCommitInput{Receipt: db.receipt, Target: commitClientTarget(db.receipt)}, state: "delegated_pending"}
	return db, c, newPublicationHumanCommitHandler(db, c, true)
}
func humanCommitRequest(t *testing.T, action, body string, uid int64) *http.Request {
	t.Helper()
	method := "POST"
	if action == "status" {
		method = "GET"
	}
	r := publicationReadRequest(t, method, SkillHubPublicationHumanCommitPrefix+"entries/"+publicationReadTestID+"/"+action, uid)
	if body != "" {
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}
func humanCommitRun(t *testing.T, h *SkillHubPublicationHumanCommitHandler, r *http.Request, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.Handle(w, r)
	if w.Code != want {
		t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("not private")
	}
	return w
}
func humanConfirmBody(c *humanCommitCloud) string {
	hash, _ := c.in.IdentityHash()
	return `{"schemaVersion":1,"confirmationKey":"` + hash + `"}`
}

func TestSkillHubPublicationHumanCommitAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		uid    int64
		status int
	}{
		{"anonymous", 0, 401}, {"other human", 2, 404}, {"bot", 3, 403}, {"disabled", 4, 403}, {"service", 5, 403}, {"deleted", 6, 401}} {
		t.Run(tc.name, func(t *testing.T) {
			db, c, h := humanCommitFixture()
			c.plan = func(context.Context) error { t.Fatal("unauthorized Cloud call"); return nil }
			humanCommitRun(t, h, humanCommitRequest(t, "plan", `{"schemaVersion":1,"confirmVersionPublish":false}`, tc.uid), tc.status)
			if db.records != 0 {
				t.Fatal("unauthorized intent")
			}
		})
	}
	for _, mutate := range []func(*http.Request){func(r *http.Request) { r.Header.Add("Authorization", r.Header.Get("Authorization")) },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer pubupload_secret") },
		func(r *http.Request) {
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: "session", Value: "secret"})
		}} {
		_, _, h := humanCommitFixture()
		r := humanCommitRequest(t, "status", "", 1)
		mutate(r)
		humanCommitRun(t, h, r, 401)
	}
}

func TestSkillHubPublicationHumanCommitExactBodyAndDefaultOff(t *testing.T) {
	for _, body := range []string{`{}`, `{"schemaVersion":1,"confirmVersionPublish":null}`, `{"schemaVersion":1,"confirmVersionPublish":false,"actorUid":"1"}`,
		`{"schemaVersion":1,"confirmVersionPublish":false,"confirmVersionPublish":true}`, `{"SchemaVersion":1,"confirmVersionPublish":false}`,
		`{"schemaVersion":1,"confirmVersionPublish":"true"}`, `{"schemaVersion":1,"confirmVersionPublish":false} {}`, strings.Repeat("x", 2049)} {
		db, c, h := humanCommitFixture()
		c.plan = func(context.Context) error { t.Fatal("bad body reached peer"); return nil }
		humanCommitRun(t, h, humanCommitRequest(t, "plan", body, 1), 400)
		if db.records != 0 {
			t.Fatal("intent on invalid body")
		}
	}
	db, _, h := humanCommitFixture()
	h.enabled = false
	humanCommitRun(t, h, humanCommitRequest(t, "status", "", 1), 404)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED", "")
	humanCommitRun(t, NewSkillHubPublicationHumanCommitHandlerFromEnv(db), humanCommitRequest(t, "status", "", 1), 404)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED", "true")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_ISSUER", "")
	humanCommitRun(t, NewSkillHubPublicationHumanCommitHandlerFromEnv(db), humanCommitRequest(t, "status", "", 1), 503)
}

func TestSkillHubPublicationHumanCommitPlanConfirmAndRecovery(t *testing.T) {
	db, c, h := humanCommitFixture()
	authorized := 0
	c.authorize = func(context.Context) error {
		authorized++
		if db.intent == nil || db.intent.Input != c.in {
			t.Fatal("authorization before exact intent")
		}
		if authorized == 1 {
			return errors.New("private token SQL")
		}
		return nil
	}
	w := humanCommitRun(t, h, humanCommitRequest(t, "plan", `{"schemaVersion":1,"confirmVersionPublish":false}`, 1), 200)
	if db.records != 0 || authorized != 0 {
		t.Fatal("plan is not consent")
	}
	var response struct {
		Plan struct {
			ConfirmationKey string `json:"confirmationKey"`
		} `json:"plan"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal("plan JSON")
	}
	hash, _ := c.in.IdentityHash()
	if response.Plan.ConfirmationKey != hash {
		t.Fatal("binding")
	}
	for _, secret := range []string{"private-", "requestHash", "manifestHash", "publisherUserId", "pubgrant_", "pubtask_"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("private plan leak", secret)
		}
	}
	body := humanConfirmBody(c)
	w = humanCommitRun(t, h, humanCommitRequest(t, "confirm", body, 1), 503)
	if strings.Contains(w.Body.String(), "private token") || db.records != 1 {
		t.Fatal("uncertain write handling")
	}
	// New HTTP handler after lost reply: same plan/key, same durable intent.
	h = newPublicationHumanCommitHandler(db, c, true)
	w = humanCommitRun(t, h, humanCommitRequest(t, "confirm", body, 1), 202)
	if db.records != 1 || authorized != 2 || strings.Contains(w.Body.String(), `"published"`) {
		t.Fatal("duplicate/success confusion")
	}
	c.state = "published"
	c.result = &PublicationCommitCloudResult{SkillID: c.in.Target.SkillID, Version: c.in.Target.Version,
		PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64), PublishedAt: c.in.Target.ReleaseAt}
	w = humanCommitRun(t, h, humanCommitRequest(t, "status", "", 1), 200)
	if !strings.Contains(w.Body.String(), `"executionState":"published"`) {
		t.Fatal("missing final status")
	}
	if strings.Contains(w.Body.String(), "permitId") || strings.Contains(w.Body.String(), "manifestHash") {
		t.Fatal("internal proof disclosed")
	}
}

func TestSkillHubPublicationHumanCommitRejectsChangedPlanAndAuthority(t *testing.T) {
	for _, mode := range []string{"changed_target", "cancelled", "record_denied", "record_failed", "loss_during_plan", "loss_during_status", "receipt_changed"} {
		t.Run(mode, func(t *testing.T) {
			db, c, h := humanCommitFixture()
			body := humanConfirmBody(c)
			action := "confirm"
			want := 409
			c.authorize = func(context.Context) error { t.Fatal("must not authorize"); return nil }
			switch mode {
			case "changed_target":
				c.in.Target.Version = "2.0.0"
			case "cancelled":
				c.state = "cancelled"
			case "record_denied":
				db.recordErr = store.ErrSkillHubPublicationDenied
				want = 403
			case "record_failed":
				db.recordErr = errors.New("private SQL")
				want = 503
			case "loss_during_plan":
				action = "plan"
				body = `{"schemaVersion":1,"confirmVersionPublish":false}`
				c.plan = func(context.Context) error { db.denied = true; return nil }
				want = 503
			case "loss_during_status":
				action = "status"
				body = ""
				c.read = func(context.Context) error { db.denied = true; return nil }
				want = 503
			case "receipt_changed":
				c.in.Receipt.EventID = "pubevent_99999999-1234-4123-8123-123456789abc"
				want = 503
			}
			humanCommitRun(t, h, humanCommitRequest(t, action, body, 1), want)
			if db.records != 0 {
				t.Fatal("unexpected intent")
			}
		})
	}
}

func TestSkillHubPublicationHumanCommitBoundedConcurrency(t *testing.T) {
	_, c, h := humanCommitFixture()
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var wg sync.WaitGroup
	c.read = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// One slot represents another entry's ongoing request; this entry's burst
	// permits three. A different entry must still respect the process cap.
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); humanCommitRun(t, h, humanCommitRequest(t, "status", "", 1), 200) }()
	}
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			wg.Wait()
			t.Fatal("not entered")
		}
	}
	r := humanCommitRequest(t, "status", "", 1)
	r.URL.Path = strings.Replace(r.URL.Path, publicationReadTestID, "pubentry_"+strings.Repeat("f", 32), 1)
	humanCommitRun(t, h, r, 503)
	close(release)
	wg.Wait()
}

func TestSkillHubPublicationHumanCommitRoutesAndResultConsistency(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		mutate func(*http.Request)
	}{
		{"wrong method", 405, func(r *http.Request) { r.Method = "GET" }},
		{"encoded path", 404, func(r *http.Request) { r.URL.RawPath = r.URL.Path }},
		{"unknown route", 404, func(r *http.Request) { r.URL.Path += "/unknown" }},
		{"query", 400, func(r *http.Request) { r.URL.RawQuery = "actorUid=1" }},
		{"encoding", 400, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{"duplicate type", 400, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, h := humanCommitFixture()
			c.plan = func(context.Context) error { t.Fatal("bad route reached peer"); return nil }
			r := humanCommitRequest(t, "plan", `{"schemaVersion":1,"confirmVersionPublish":false}`, 1)
			tc.mutate(r)
			humanCommitRun(t, h, r, tc.status)
		})
	}
	for _, state := range []string{"ready", "published", "delegated_pending"} {
		_, c, h := humanCommitFixture()
		c.state = state
		if state == "delegated_pending" {
			c.result = &PublicationCommitCloudResult{SkillID: "unverified"}
		}
		humanCommitRun(t, h, humanCommitRequest(t, "status", "", 1), 503)
	}
	_, _, h := humanCommitFixture()
	r := humanCommitRequest(t, "status", "", 1)
	r.AddCookie(&http.Cookie{Name: "incidental", Value: "not-a-credential"})
	humanCommitRun(t, h, r, 200)
}

func TestSkillHubPublicationHumanCommitCancelledContextDoesNotRecord(t *testing.T) {
	db, c, h := humanCommitFixture()
	r := humanCommitRequest(t, "confirm", humanConfirmBody(c), 1)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	c.read = func(context.Context) error { cancel(); return nil }
	c.authorize = func(context.Context) error { t.Fatal("cancelled request authorized"); return nil }
	humanCommitRun(t, h, r, 503)
	if db.records != 0 {
		t.Fatal("cancelled context wrote intent")
	}
}
