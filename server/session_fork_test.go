package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type forkTestStore struct {
	store.Store
	record       *types.SessionFork
	apiKey       string
	completeBody string
}

func (s *forkTestStore) GetUser(id int64) (*types.User, error) {
	account := types.AccountHuman
	if id == 9 {
		account = types.AccountBot
	}
	return &types.User{ID: id, AccountType: account}, nil
}
func (s *forkTestStore) GetBotByAPIKey(key string) (int64, error) {
	if key == s.apiKey && key != "" {
		return 9, nil
	}
	return 0, errors.New("invalid key")
}
func (s *forkTestStore) GetSessionFork(ctx context.Context, uid int64, request, topic string) (*types.SessionFork, error) {
	if s.record != nil && s.record.ActorUserID == uid {
		return s.record, nil
	}
	return nil, store.ErrSessionForkNotFound
}
func (s *forkTestStore) CreateSessionFork(context.Context, types.SessionForkRequest) (*types.SessionFork, error) {
	panic("unexpected create")
}
func (s *forkTestStore) GetBotSessionFork(_ context.Context, uid int64, topic string) (*types.SessionFork, error) {
	if s.record != nil && s.record.AgentUID == uid && s.record.TargetTopicID == topic {
		return s.record, nil
	}
	return nil, store.ErrSessionForkNotFound
}
func (s *forkTestStore) CompleteSessionFork(_ context.Context, uid int64, req types.SessionForkCompletion) (*types.SessionFork, error) {
	s.completeBody = req.BodyID
	return s.GetBotSessionFork(context.Background(), uid, req.TargetTopicID)
}
func (s *forkTestStore) SessionForkWritable(context.Context, string) (bool, error) { return false, nil }

func TestSessionForkGetIsUserScoped(t *testing.T) {
	db := &forkTestStore{record: &types.SessionFork{ActorUserID: 7, RequestID: "one", TargetTopicID: "grp_20", State: "pending"}}
	h := NewConversationHandler(db, nil)
	for _, tc := range []struct {
		uid  int64
		code int
	}{{7, 200}, {8, 404}} {
		r := httptest.NewRequest(http.MethodGet, "/api/conversations/fork?request_id=one", nil)
		r = r.WithContext(context.WithValue(r.Context(), uidKey, tc.uid))
		w := httptest.NewRecorder()
		h.HandleFork(w, r)
		if w.Code != tc.code {
			t.Fatalf("uid %d: %d %s", tc.uid, w.Code, w.Body.String())
		}
	}
}

func TestSessionForkPendingRejectsHTTPAndWebSocket(t *testing.T) {
	db := &forkTestStore{}
	h := NewHub(db, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/messages/send", strings.NewReader(`{"topic_id":"grp_20","content":"hello"}`))
	r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
	w := httptest.NewRecorder()
	NewMessageHandler(db, h).HandleSendMessage(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	client := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 4)}
	h.handlePub(client, &MsgClientPub{ID: "m", Topic: "grp_20", Content: []byte(`"hello"`)})
	select {
	case response := <-client.send:
		if !strings.Contains(string(response), `"code":409`) {
			t.Fatalf("WS %s", response)
		}
	default:
		t.Fatal("missing rejection")
	}
}

func TestSessionForkAuthSeparationAndBotProvenance(t *testing.T) {
	db := &forkTestStore{apiKey: GenerateAPIKey(9), record: &types.SessionFork{ActorUserID: 7, AgentUID: 9, RequestID: "one", SourceTopicID: "p2p_7_9", TargetTopicID: "grp_20", State: "pending"}}
	h := NewConversationHandler(db, nil)
	jwt, err := GenerateToken(7, "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body, token, key string
		handler                              http.HandlerFunc
		want                                 int
	}{
		{"user JWT get", "GET", "/api/conversations/fork?request_id=one", "", jwt, "", JWTAuthMiddlewareWithDB(db)(h.HandleFork), 200},
		{"bot key denied user route", "GET", "/api/conversations/fork?request_id=one", "", "", db.apiKey, JWTAuthMiddlewareWithDB(db)(h.HandleFork), 401},
		{"JWT cannot complete", "POST", "/api/bot/session-forks/complete", `{"request_id":"one","target_topic_id":"grp_20","state":"ready"}`, jwt, "", BotAPIKeyMiddlewareWithDB(db)(h.HandleForkComplete), 401},
		{"bot completes", "POST", "/api/bot/session-forks/complete", `{"request_id":"one","target_topic_id":"grp_20","state":"ready"}`, "", db.apiKey, BotAPIKeyMiddlewareWithDB(db)(h.HandleForkComplete), 200},
		{"bot provenance", "GET", "/api/bot/session-forks?topic_id=grp_20", "", "", db.apiKey, BotAPIKeyMiddlewareWithDB(db)(h.HandleBotFork), 200},
		{"ordinary topic", "GET", "/api/bot/session-forks?topic_id=grp_21", "", "", db.apiKey, BotAPIKeyMiddlewareWithDB(db)(h.HandleBotFork), 404},
		{"JWT cannot get bot provenance", "GET", "/api/bot/session-forks?topic_id=grp_20", "", jwt, "", BotAPIKeyMiddlewareWithDB(db)(h.HandleBotFork), 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.key != "" {
				r.Header.Set("Authorization", "ApiKey "+tc.key)
			}
			r.Header.Set(botBodyIDHeader, "body-test")
			w := httptest.NewRecorder()
			tc.handler(w, r)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.want == 404 && !strings.Contains(w.Body.String(), `"error":"session_fork_not_found"`) {
				t.Fatalf("restore sentinel %s", w.Body.String())
			}
		})
	}
	if db.completeBody != "body-test" {
		t.Fatal("completion body header not forwarded")
	}
}

func TestSessionForkRetryReturnsTerminalWithoutSourceOrOnlineLookup(t *testing.T) {
	for _, state := range []string{"ready", "failed", "pending"} {
		t.Run(state, func(t *testing.T) {
			db := &forkTestStore{record: &types.SessionFork{ActorUserID: 7, AgentUID: 9, RequestID: "same", SourceTopicID: "p2p_7_9", TargetTopicID: "grp_20", State: state}}
			r := httptest.NewRequest("POST", "/api/conversations/fork", strings.NewReader(`{"request_id":"same","source_topic_id":"p2p_7_9"}`))
			r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
			w := httptest.NewRecorder()
			NewConversationHandler(db, nil).HandleFork(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"`+state+`"`) {
				t.Fatalf("retry %d %s", w.Code, w.Body.String())
			}
			r = httptest.NewRequest("POST", "/api/conversations/fork", strings.NewReader(`{"request_id":"same","source_topic_id":"p2p_7_9","name":"different"}`))
			r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
			w = httptest.NewRecorder()
			NewConversationHandler(db, nil).HandleFork(w, r)
			if w.Code != 409 {
				t.Fatalf("payload conflict %d", w.Code)
			}
		})
	}
}

func TestSessionForkControlIsNotAChatMessage(t *testing.T) {
	db := &forkTestStore{}
	hub := NewHub(db, nil)
	if _, err := hub.bodyLeases.acquire(9, "body-test", "connection-test"); err != nil {
		t.Fatal(err)
	}
	client := &Client{uid: 9, accountType: types.AccountBot, bodyID: "body-test", connectionID: "connection-test", send: make(chan []byte, 4)}
	hub.clients[9] = map[*Client]struct{}{client: {}}
	f := &types.SessionFork{ActorUserID: 7, AgentUID: 9, BodyID: "body-test", RequestID: "one", SourceTopicID: "p2p_7_9", TargetTopicID: "grp_20", State: "pending", SourceLastSeq: 31, MessageIDMap: map[string]int64{"30": 40, "31": 41}}
	NewConversationHandler(db, hub).dispatchSessionFork(f)
	select {
	case raw := <-client.send:
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope) != 1 || envelope["session_fork"] == nil {
			t.Fatalf("chat side effect: %s", raw)
		}
		var control types.SessionForkControl
		if err := json.Unmarshal(envelope["session_fork"], &control); err != nil {
			t.Fatal(err)
		}
		if control.SourceLastSeq != 31 || control.MessageIDMap["30"] != 40 || control.ActorUserID != 7 || control.AgentUID != 9 {
			t.Fatalf("wire contract %#v", control)
		}
	default:
		t.Fatal("missing bot control")
	}
	f.BodyID = "another-body"
	NewConversationHandler(db, hub).dispatchSessionFork(f)
	select {
	case <-client.send:
		t.Fatal("control sent to wrong body")
	default:
	}
}

func TestSessionForkFenceCoversTransientAndStreamPublish(t *testing.T) {
	db := &forkTestStore{}
	hub := NewHub(db, nil)
	for _, kind := range []string{"stream_delta", "stream_cancel", "task_status", "runtime_plan"} {
		t.Run(kind, func(t *testing.T) {
			client := &Client{uid: 9, accountType: types.AccountBot, send: make(chan []byte, 4)}
			hub.handlePub(client, &MsgClientPub{ID: "x", Topic: "grp_20", Type: kind, Content: []byte(`"ignored"`), Metadata: map[string]interface{}{"stream_id": "stream"}})
			select {
			case raw := <-client.send:
				if !strings.Contains(string(raw), `"code":409`) {
					t.Fatalf("WS %s", raw)
				}
			default:
				t.Fatal("missing rejection")
			}
			body := `{"topic_id":"grp_20","content":"ignored","type":"` + kind + `"}`
			r := httptest.NewRequest("POST", "/api/messages/send", strings.NewReader(body))
			r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(9)))
			w := httptest.NewRecorder()
			NewMessageHandler(db, hub).HandleSendMessage(w, r)
			if w.Code != 409 {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSessionForkHistoryExposesRemappedReplies(t *testing.T) {
	hub := NewHub(&forkTestStore{}, nil)
	message := &types.Message{ID: 101, TopicID: "grp_20", FromUID: 9, Content: "reply", MsgType: "text", ReplyTo: 100}
	ws := hub.historyMessageDataForRecipient(7, message)
	if ws.ReplyTo != 100 {
		t.Fatalf("WebSocket reply = %d", ws.ReplyTo)
	}
	httpMessage := hub.historyAPIMessageForRecipient(7, message)
	if httpMessage["reply_to"] != int(100) {
		t.Fatalf("HTTP reply = %#v", httpMessage["reply_to"])
	}
}
