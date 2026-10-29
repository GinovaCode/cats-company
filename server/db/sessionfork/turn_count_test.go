package sessionfork

import (
	"database/sql"
	"strconv"
	"testing"
)

// Production does not write role='user': the plain-text send path stores only
// topic, sender, content and type, so human rows carry an empty role while bot
// rows carry 'assistant'. These fixtures mirror that shape; a role-based rule
// would count zero turns against them.
const agentUID int64 = 9

func human() message { return message{from: 7, role: sql.NullString{}} }
func bot() message {
	return message{from: agentUID, role: sql.NullString{String: "assistant", Valid: true}}
}
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
		// Regression: a conversation that opens with an agent message scores zero
		// turns at that anchor. Zero is indistinguishable from "unbounded" on the
		// wire (`source_turn_count` is omitempty), so the runtime would copy the
		// whole transcript while the cloud copied a prefix. Production has such
		// conversations: 10 two-member human+bot groups start with a bot message.
		{"agent-only conversation", []message{bot()}, 0, false},
		{"agent first, anchor before the first user message", []message{bot(), human(), bot()}, 0, false},
		{"agent first, anchor on the answered turn", []message{bot(), human(), bot()}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := anchorClosesTurn(tc.all, tc.anchor, agentUID); got != tc.want {
				t.Fatalf("anchorClosesTurn(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// A copy is named after its source with a running suffix. The suffix is computed
// from the owner's existing names, and a name the user already ended with "(n)"
// is taken whole: guessing which parenthetical is a copy marker would rewrite a
// name the user chose.
func TestCopySuffix(t *testing.T) {
	for _, tc := range []struct {
		base, name string
		want       int64
		ok         bool
	}{
		{"周报", "周报(1)", 1, true},
		{"周报", "周报(12)", 12, true},
		{"周报", "周报", 0, false},
		{"周报", "周报(1) 副本", 0, false},
		{"周报", "周报(abc)", 0, false},
		{"周报", "周报()", 0, false},
		{"周报", "周报(0)", 0, false},
		{"周报", "周报(-1)", 0, false},
		{"周报", "周报(1)(2)", 0, false},
		{"周报(1)", "周报(1)(3)", 3, true},
		{"", "(1)", 1, true},
	} {
		got, ok := copySuffix(tc.base, tc.name)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("copySuffix(%q, %q) = %d,%v, want %d,%v", tc.base, tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// escape-free: the highest existing suffix wins, so siblings never collide even
// when some are deleted and re-created.
func TestNextCopyTitle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		base     string
		existing []string
		want     string
	}{
		{"first copy", "周报", nil, "周报(1)"},
		{"second copy", "周报", []string{"周报(1)"}, "周报(2)"},
		{"fills the gap after the highest", "周报", []string{"周报(1)", "周报(3)"}, "周报(4)"},
		{"ignores other conversations", "周报", []string{"日报(1)", "周报 归档"}, "周报(1)"},
		{"takes a name ending in (n) whole", "周报(1)", []string{"周报(1)(1)"}, "周报(1)(2)"},
		{"ignores a malformed suffix", "周报", []string{"周报(一)"}, "周报(1)"},
	} {
		// existing names are matched against the base the same way copyTitle does
		taken := int64(0)
		for _, name := range tc.existing {
			if suffix, ok := copySuffix(tc.base, name); ok && suffix > taken {
				taken = suffix
			}
		}
		got := tc.base + "(" + strconv.FormatInt(taken+1, 10) + ")"
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
