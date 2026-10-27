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

// A copy may only be cut where the turn actually ends. The regression this
// guards: a reply longer than the 4000-character segment limit is stored as
// several messages, so one turn reads [user, agent, agent, agent] in the cloud. An
// anchor on the first agent segment is "answered" but is not the turn's end, and
// accepting it truncated the copy while the runtime kept the whole turn — the very
// mismatch between visible history and model context this feature must avoid.
func TestAnchorClosesTurn(t *testing.T) {
	segmented := []message{human(), bot(), bot(), bot()}
	for _, tc := range []struct {
		name   string
		all    []message
		anchor int
		want   bool
	}{
		{"single answer closes the turn", []message{human(), bot()}, 1, true},
		// The regression: the first of three reply segments must be rejected.
		{"first of three reply segments", segmented, 1, false},
		{"middle of three reply segments", segmented, 2, false},
		{"last of three reply segments", segmented, 3, true},
		{"anchor on the user message", []message{human(), bot()}, 0, false},
		{"more turns follow", []message{human(), bot(), human(), bot()}, 1, true},
		{"anchor on trailing user message", []message{human(), bot(), human()}, 2, false},
		{"agent message before more agent work", []message{human(), bot(), bot(), human()}, 1, false},
		{"out of range", []message{human(), bot()}, 5, false},
		{"empty conversation", nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := anchorClosesTurn(tc.all, tc.anchor, agentUID); got != tc.want {
				t.Fatalf("anchorClosesTurn(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
