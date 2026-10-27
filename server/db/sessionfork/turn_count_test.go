package sessionfork

import (
	"database/sql"
	"testing"
)

// Production does not write role='user': the plain-text send path stores only
// topic, sender, content and type, so human rows carry an empty role while bot
// rows carry 'assistant'. These fixtures mirror that shape; a role-based rule
// would count zero turns against them.
const agentUID int64 = 9

func human() message { return message{from: 7, role: sql.NullString{}} }
func bot() message   { return message{from: agentUID, role: sql.NullString{String: "assistant", Valid: true}} }
func botPlain() message {
	return message{from: agentUID, role: sql.NullString{}}
}

func TestCountCompleteTurns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []message
		want     int64
	}{
		{"empty range", nil, 0},
		{"single answered turn", []message{human(), bot()}, 1},
		{"two answered turns", []message{human(), bot(), human(), bot()}, 2},
		// The runtime still holds this trailing turn. Counting it would make the
		// runtime cut one turn further than the cloud range intends.
		{"trailing unanswered user", []message{human(), bot(), human()}, 1},
		// A user sending twice before any answer is one turn, not two. Both sides
		// must fold it the same way or the runtime cuts between the two messages.
		{"consecutive user messages fold", []message{human(), human(), bot()}, 1},
		{"assistant only", []message{bot(), bot()}, 0},
		// Any agent message answers the open turn, even without a role: production
		// writes ~6% of agent rows with an empty role, and requiring
		// role='assistant' would undercount turns here while the runtime counted
		// them.
		{"agent without role still answers", []message{human(), botPlain()}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := countCompleteTurns(tc.messages, agentUID); got != tc.want {
				t.Fatalf("countCompleteTurns(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// The regression this guards: counting by `role` alone returns zero on real
// data, which made every bounded copy fail its anchor check and every turn count
// read as "unbounded".
func TestCountCompleteTurnsOnProductionShape(t *testing.T) {
	history := []message{human(), bot(), human(), bot()}
	if got := countCompleteTurns(history, agentUID); got != 2 {
		t.Fatalf("production-shaped history counts %d turns, want 2", got)
	}
}

// The copy boundary must not end in a turn that is still waiting for an answer,
// and it must not reject a legal anchor either. Comparing "count of range" with
// "count without the last row" got the second case wrong: an anchor on the last of
// several agent messages leaves the count unchanged.
func TestEndsWithOpenTurn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []message
		want     bool
	}{
		{"answered turn", []message{human(), bot()}, false},
		{"trailing user message", []message{human(), bot(), human()}, true},
		{"anchor on last of several agent messages", []message{human(), bot(), bot()}, false},
		{"only a user message", []message{human()}, true},
		{"empty range", nil, false},
		{"agent first", []message{bot(), human(), bot()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := endsWithOpenTurn(tc.messages, agentUID); got != tc.want {
				t.Fatalf("endsWithOpenTurn(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
