package server

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

// sessionForkAckTimeout bounds how long a copy may stay pending while the
// runtime that received the control frame never acknowledges it.
//
// This is the compatibility gate for session forks. A runtime built before the
// feature existed receives the frame, ignores it without reporting anything,
// and never calls the completion endpoint, so without a bound the copy stays
// pending forever and the client polls it indefinitely. A healthy runtime
// acknowledges in seconds: copying is a local context snapshot with no model
// turn, and the server already refuses to create a copy while the source turn
// is running. The window therefore only has to absorb a slow disk or a brief
// reconnect, not a model call.
const sessionForkAckTimeoutDefault = 2 * time.Minute

// sessionForkReaperInterval is how often expired pending copies are closed. It
// is deliberately shorter than the timeout so the client sees the terminal
// state within roughly one interval of the deadline.
const sessionForkReaperInterval = 30 * time.Second

// sessionForkAckTimeout resolves the acknowledgement window, allowing an
// operator to widen it for a slow deployment without a code change.
func sessionForkAckTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CATS_SESSION_FORK_ACK_TIMEOUT_SECONDS"))
	if raw == "" {
		return sessionForkAckTimeoutDefault
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return sessionForkAckTimeoutDefault
	}
	return time.Duration(seconds) * time.Second
}

// runSessionForkReaper closes pending copies whose runtime never acknowledged
// them. It complements the per-request dispatch: the create path dispatches
// once and waits, so a silently ignored frame would otherwise leave the copy
// pending for the lifetime of the deployment.
func (h *Hub) runSessionForkReaper() {
	if h == nil || h.db == nil {
		return
	}
	forks, ok := h.db.(store.SessionForkStore)
	if !ok {
		return
	}
	interval := h.taskReaperInterval
	if interval <= 0 {
		interval = sessionForkReaperInterval
	}
	reap := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := forks.ExpireSessionForks(ctx, time.Now().UTC().Add(-sessionForkAckTimeout()), 0)
		cancel()
		if err != nil {
			log.Printf("session fork reaper: %v", err)
		}
	}
	reap()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		reap()
	}
}
