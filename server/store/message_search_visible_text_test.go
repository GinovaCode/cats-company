package store

import (
	"strings"
	"testing"
)

func TestMessageSearchVisibleText(t *testing.T) {
	tests := []struct {
		name, content, blocks, query, want string
	}{
		{"blocks-only text", "", `[{"type":"text","text":"部署成功"}]`, "部署", "部署成功"},
		{"blocks-only assistant answer", "", `[{"type":"assistant_text","text":"部署成功"}]`, "部署", "部署成功"},
		{"content field fallback", "", `[{"type":"assistant_text","content":"部署成功"}]`, "部署", "部署成功"},
		{"empty text field fallback", "", `[{"type":"text","text":"","content":"部署成功"}]`, "部署", "部署成功"},
		{"text field takes precedence", "", `[{"type":"text","text":"部署成功","content":"hidden fallback"}]`, "部署", "部署成功"},
		{"case-insensitive block text", "", `[{"type":"text","text":"Deployment SUCCESS"}]`, "success", "Deployment SUCCESS"},
		{"thinking with answer", "private legacy body", `[{"type":"thinking","thinking":"private reasoning"},{"type":"assistant_text","text":"部署成功"}]`, "部署", "部署成功"},
		{"tool result with answer", "private tool output", `[{"type":"tool_result","content":"private output"},{"type":"text","text":"部署成功"}]`, "部署", "部署成功"},
		{"multiple text blocks", "private legacy body", `[{"type":"text","text":"第一段"},{"type":"thinking","thinking":"private reasoning"},{"type":"assistant_text","text":"部署成功"}]`, "部署", "第一段\n部署成功"},
		{"long text remains searchable", "", `[{"type":"text","text":"` + strings.Repeat("前", 2200) + `部署成功"}]`, "部署", strings.Repeat("前", 2200) + "部署成功"},
		{"legacy plain text", "部署成功", "", "部署", "部署成功"},
		{"legacy JSON null blocks", "部署成功", "null", "部署", "部署成功"},
		{"legacy empty blocks", "部署成功", "[]", "部署", "部署成功"},
		{"legacy empty text block", "部署成功", `[{"type":"text","text":""}]`, "部署", "部署成功"},
		{"legacy attachment caption", "部署成功", `[{"type":"file","name":"report.pdf"}]`, "部署", "部署成功"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := MessageSearchCandidate{Result: MessageSearchResult{Content: tt.content}, MessageType: "text", ContentBlocks: []byte(tt.blocks)}
			for _, scope := range []string{MessageSearchAll, MessageSearchMessage} {
				result, ok := MatchMessageSearchCandidate(candidate, tt.query, scope)
				if !ok {
					t.Fatalf("scope=%s: visible text did not match", scope)
				}
				if result.Content != tt.want || !strings.Contains(strings.ToLower(result.Snippet), strings.ToLower(tt.query)) || result.ContentType != MessageSearchMessage {
					t.Fatalf("scope=%s: result=%+v, want visible content %q", scope, result, tt.want)
				}
			}
			if _, ok := MatchMessageSearchCandidate(candidate, tt.query, MessageSearchArtifact); ok {
				t.Fatal("artifact-only search included a text match")
			}
		})
	}
}

func TestMessageSearchDoesNotExposePrivateText(t *testing.T) {
	for _, blocks := range []string{
		`[{"type":"thinking","thinking":"private"},{"type":"assistant_text","text":"visible answer"}]`,
		`[{"type":"tool_use","name":"private"},{"type":"text","text":"visible answer"}]`,
		`[{"type":"tool_result","content":"private"},{"type":"text","text":"visible answer"}]`,
		`[{"type":"runtime_plan","content":"private"},{"type":"text","text":"visible answer"}]`,
		`[{"type":"debug","content":"private"},{"type":"text","text":"visible answer"}]`,
		`[{"type":"text","text":"visible answer","content":"private"}]`,
		`[{"type":"text","text":"visible answer"}]`,
		`[{"type":"text","text":" ","content":"private"}]`,
		`[{"type":"thinking","thinking":"private"}]`,
		`[{"type":"debug","content":"private"}]`,
	} {
		t.Run(blocks, func(t *testing.T) {
			candidate := MessageSearchCandidate{Result: MessageSearchResult{Content: "private legacy body"}, MessageType: "text", ContentBlocks: []byte(blocks)}
			if result, ok := MatchMessageSearchCandidate(candidate, "private", MessageSearchAll); ok {
				t.Fatalf("private text became a search hit: %+v", result)
			}
		})
	}
	candidate := MessageSearchCandidate{
		Result: MessageSearchResult{Content: "private legacy body"}, MessageType: "text",
		ContentBlocks: []byte(`[{"type":"tool_result","content":"private output"},{"type":"file","name":"Visible Report.pdf"}]`),
	}
	result, ok := MatchMessageSearchCandidate(candidate, "report", MessageSearchArtifact)
	if !ok || result.Content != "Visible Report.pdf" || result.Snippet != result.Content {
		t.Fatalf("artifact result must expose only its verified name: %+v", result)
	}
}
