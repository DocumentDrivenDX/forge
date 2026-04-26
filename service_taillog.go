package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

// sessionHub is a concurrent-safe broadcast store for in-flight and completed
// sessions. It is held on the service struct and used by both Execute (to
// register/broadcast events) and TailSessionLog (to subscribe).
//
// Lifecycle:
//  1. Execute registers a session via openSession before the goroutine starts.
//  2. The runExecute goroutine calls broadcastEvent for every event it emits.
//  3. When runExecute is done (channel closes), it calls closeSession, which
//     marks the session complete and closes all subscriber channels.
//  4. TailSessionLog calls subscribe, which returns either an active subscriber
//     channel (for in-progress sessions) or a one-shot channel that immediately
//     yields the stored final event then closes (for completed sessions).
type sessionHub struct {
	mu       sync.Mutex
	sessions map[string]*hubSession
}

type hubSession struct {
	// done is true once the session has ended (all events emitted).
	done bool
	// finalEvent is the last event emitted (type=final). Stored for late
	// subscribers that attach after the session ends.
	finalEvent *ServiceEvent
	// subscribers receive a copy of every future broadcast. Each subscriber
	// has its own buffered channel; slow consumers are dropped (non-blocking
	// send with overflow discard).
	subscribers []chan ServiceEvent
}

func newSessionHub() *sessionHub {
	return &sessionHub{sessions: make(map[string]*hubSession)}
}

// openSession registers a new session. Must be called before Execute starts
// its goroutine so that TailSessionLog callers that arrive immediately after
// Execute returns can observe the session.
func (h *sessionHub) openSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[sessionID] = &hubSession{}
}

// broadcastEvent sends ev to all active subscribers for sessionID. Slow
// subscribers (full channel) are skipped to avoid blocking Execute.
func (h *sessionHub) broadcastEvent(sessionID string, ev ServiceEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess, ok := h.sessions[sessionID]
	if !ok || sess.done {
		return
	}
	for _, sub := range sess.subscribers {
		select {
		case sub <- ev:
		default:
			// Subscriber channel full — discard to avoid blocking Execute.
		}
	}
}

// closeSession marks the session done, stores the final event, and closes
// all subscriber channels. After this call no new events will be broadcast.
func (h *sessionHub) closeSession(sessionID string, finalEv ServiceEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess, ok := h.sessions[sessionID]
	if !ok {
		return
	}
	sess.done = true
	sess.finalEvent = &finalEv
	for _, sub := range sess.subscribers {
		close(sub)
	}
	sess.subscribers = nil
}

// subscribe returns a channel that will receive all subsequent events for
// sessionID and then be closed when the session ends. If the session is
// already complete, the channel receives the stored final event then closes
// immediately. Returns an error for unknown session IDs.
func (h *sessionHub) subscribe(sessionID string) (<-chan ServiceEvent, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	sess, ok := h.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("unknown session %q", sessionID)
	}

	ch := make(chan ServiceEvent, 32)

	if sess.done {
		// Session already completed: deliver final event then close.
		go func() {
			if sess.finalEvent != nil {
				ch <- *sess.finalEvent
			}
			close(ch)
		}()
		return ch, nil
	}

	// Session in progress: register as a subscriber.
	sess.subscribers = append(sess.subscribers, ch)
	return ch, nil
}

// TailSessionLog streams events from an in-progress or completed session by
// ID. Multiple concurrent callers on the same sessionID each receive the full
// remaining event stream. Callers attached after completion receive the stored
// final event then see the channel close. Returns an error for unknown IDs.
func (s *service) TailSessionLog(ctx context.Context, sessionID string) (<-chan ServiceEvent, error) {
	ch, err := s.hub.subscribe(sessionID)
	if err != nil {
		return nil, err
	}

	// Wrap ch so ctx cancellation closes our output channel without leaking
	// the subscriber slot. We drain into a proxy channel so callers see ctx
	// cancellation as a clean channel close (not a blocked read).
	proxy := make(chan ServiceEvent, 32)
	go func() {
		defer close(proxy)
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				select {
				case proxy <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return proxy, nil
}

// wrapExecuteWithHub wraps the inner out channel so that every event emitted
// by runExecute is also broadcast to TailSessionLog subscribers. Returns a
// write-only channel that Execute's goroutine writes to; reads are done by
// the fan-out goroutine which forwards to both the caller's (outer) channel
// and the hub.
//
// Usage in Execute:
//
//	outer := make(chan ServiceEvent, 64)   // returned to caller
//	inner, cleanup := s.hub.wrapExecuteWithHub(sessionID, outer)
//	go s.runExecute(ctx, req, decision, meta, inner)
//	return outer, nil
//
// The fan-out goroutine closes outer when inner closes, and calls
// closeSession on the hub with the last final event seen.
func (h *sessionHub) wrapExecuteWithHub(sessionID string, outer chan ServiceEvent, ovr *overrideContext, meta map[string]string) (inner chan ServiceEvent) {
	inner = make(chan ServiceEvent, 64)
	go func() {
		defer close(outer)
		var lastFinal ServiceEvent
		for ev := range inner {
			// ADR-006 §7: emit the override event immediately before the
			// final event so consumers correlating per-session can join
			// cleanly. Outcome is populated from the final event itself.
			if ev.Type == harnesses.EventTypeFinal && ovr != nil && !ovr.emitted.Load() {
				if overrideEv, payload, ok := makeOverrideEvent(ovr, sessionID, ev, meta); ok {
					ovr.emitted.Store(true)
					// Back-write the outcome onto the in-memory ring so
					// RouteStatus aggregates surface real success/stalled/
					// failed counts (ADR-006 §5).
					stampOutcomeOnRecord(ovr.record, payload.Outcome)
					// Persist the override event to the session log so
					// UsageReport can recompute routing-quality from
					// historical sessions across restarts.
					if sl := ovr.sl.Load(); sl != nil {
						sl.writeOverrideEvent(ServiceEventTypeOverride, payload)
					}
					select {
					case outer <- overrideEv:
					case <-time.After(5 * time.Second):
					}
					h.broadcastEvent(sessionID, overrideEv)
				}
			}
			// Forward to the caller's channel.
			select {
			case outer <- ev:
			case <-time.After(5 * time.Second):
				// Caller stopped reading — discard but keep broadcasting.
			}
			// Broadcast to hub subscribers.
			h.broadcastEvent(sessionID, ev)
			// Track the final event for post-completion subscribers.
			if ev.Type == harnesses.EventTypeFinal {
				lastFinal = ev
			}
		}
		h.closeSession(sessionID, lastFinal)
	}()
	return inner
}
