package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShimoWriterCapabilityIsSeparateFromReader(t *testing.T) {
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret,
		PublicURL:   "https://app.catsco.test",
		Backend:     mockShimoConnectorBackend{},
	})
	key := shimoConnectionKey{agentUID: "agent-42", actorUID: "user-a"}
	handler.connections[key] = shimoConnection{state: "connected"}

	readerToken := mustShimoActorToken(t, key.agentUID, key.actorUID, "task-read")
	readerWrite := callShimoJSON(t, handler.HandleUpdateSheet, http.MethodPost, "/v1/shimo/sheets/update", readerToken,
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:B1","values":[["新客户",9000]],"expected_values":[["旧客户",8000]]}`)
	assertShimoError(t, readerWrite, http.StatusForbidden, "WRITE_CAPABILITY_REQUIRED")

	writerToken, err := GenerateShimoWriterActorToken(shimoTestSecret, key.agentUID, key.actorUID, "task-write", "catsco/shimo-writer", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	writerWrite := callShimoJSONAsSkill(t, handler.HandleUpdateSheet, http.MethodPost, "/v1/shimo/sheets/update", writerToken, "catsco/shimo-writer",
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:B1","values":[["新客户",9000]],"expected_values":[["旧客户",8000]]}`)
	assertShimoStatus(t, writerWrite, http.StatusOK, true)
	if nested, ok := writerWrite.body["data"].(map[string]any); !ok || nested["verified"] != true {
		t.Fatalf("unexpected write result: %#v", writerWrite.body)
	}
}

func TestShimoWriterRejectsInvalidMatrices(t *testing.T) {
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{ActorSecret: shimoTestSecret, Backend: mockShimoConnectorBackend{}})
	handler.connections[shimoConnectionKey{agentUID: "agent-42", actorUID: "user-a"}] = shimoConnection{state: "connected"}
	token, err := GenerateShimoWriterActorToken(shimoTestSecret, "agent-42", "user-a", "task-write", "catsco/shimo-writer", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:B1","values":[],"expected_values":[]}`,
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:B2","values":[[1,2],[3]],"expected_values":[]}`,
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:A1","values":[[true]],"expected_values":[]}`,
	} {
		result := callShimoJSONAsSkill(t, handler.HandleUpdateSheet, http.MethodPost, "/v1/shimo/sheets/update", token, "catsco/shimo-writer", body)
		assertShimoError(t, result, http.StatusBadRequest, "INVALID_ARGUMENTS")
	}
}

func callShimoJSONAsSkill(t *testing.T, handler http.HandlerFunc, method, target, token, skillID, body string) shimoHTTPResult {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-CatsCo-Skill-ID", skillID)
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode status=%d body=%q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return shimoHTTPResult{status: recorder.Code, body: decoded}
}
