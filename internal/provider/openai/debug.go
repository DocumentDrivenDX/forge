package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/option"
)

// Wire-dump observability is gated by environment variables and is complementary
// to session events (agent.EventLLMRequest / EventLLMResponse). Session events
// capture the logical request/response (Messages, Tools, Content, ToolCalls).
// Wire dump captures the HTTP-layer data only: method, URL, status, headers,
// raw bodies, and SSE chunk boundaries. Do not duplicate the logical fields.
//
// Env vars:
//   AGENT_DEBUG_WIRE      — "1" or "true" enables the dump; default off
//   AGENT_DEBUG_WIRE_FILE — optional path; when set, JSONL is written to this
//                           file, otherwise events go to stderr as JSONL
//
// When disabled, no middleware is installed and overhead is zero.

const (
	envDebugWire     = "AGENT_DEBUG_WIRE"
	envDebugWireFile = "AGENT_DEBUG_WIRE_FILE"
)

// bearerTokenPattern matches "Authorization: Bearer <token>" so we can redact
// the token before any wire event is written.
var bearerTokenPattern = regexp.MustCompile(`(?i)(Bearer\s+)[^\s]+`)

// debugSink serializes wire-dump events to a single writer with a mutex. A
// single sink is shared across all providers in a process so output ordering
// is stable even under concurrent requests.
type debugSink struct {
	mu     sync.Mutex
	w      io.Writer
	closer io.Closer
	seq    uint64
}

var (
	sinkOnce sync.Once
	sink     *debugSink // nil when AGENT_DEBUG_WIRE is unset
)

// envBoolTrue reports whether the named env var is set to a truthy value
// (1, true — case-insensitive). Empty, "0", or "false" → false.
func envBoolTrue(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" || v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	return true
}

// resolveDebugSink returns the shared sink, or nil if wire dump is disabled.
func resolveDebugSink() *debugSink {
	sinkOnce.Do(func() {
		v := strings.TrimSpace(os.Getenv(envDebugWire))
		if v == "" || v == "0" || strings.EqualFold(v, "false") {
			return
		}
		if path := strings.TrimSpace(os.Getenv(envDebugWireFile)); path != "" {
			// #nosec G304 G703 -- path comes from operator-set env var; intentional.
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				fmt.Fprintf(os.Stderr, "agent: AGENT_DEBUG_WIRE_FILE open failed: %v — falling back to stderr\n", err)
				sink = &debugSink{w: os.Stderr}
				return
			}
			sink = &debugSink{w: f, closer: f}
			return
		}
		sink = &debugSink{w: os.Stderr}
	})
	return sink
}

type wireEvent struct {
	Seq        uint64      `json:"seq"`
	Ts         string      `json:"ts"`
	Dir        string      `json:"dir"` // "request" | "response" | "error"
	Method     string      `json:"method,omitempty"`
	URL        string      `json:"url,omitempty"`
	Status     int         `json:"status,omitempty"`
	Headers    http.Header `json:"headers,omitempty"`
	BodyBytes  int         `json:"body_bytes,omitempty"`
	Body       string      `json:"body,omitempty"`
	Truncated  bool        `json:"truncated,omitempty"`
	LatencyMs  int64       `json:"latency_ms,omitempty"`
	ContentTyp string      `json:"content_type,omitempty"`
	Err        string      `json:"error,omitempty"`
}

// maxBodyBytes limits how much body content a single event captures. Applies
// to request bodies and to non-streaming response bodies (which are read
// whole before being emitted).
const maxBodyBytes = 64 * 1024

// envDebugWireStreamFull opts into capturing the entire SSE stream body via
// teeBody. Default behavior caps cumulative captured bytes at maxBodyBytes to
// avoid flooding stderr during long reasoning-model generations; this knob
// disables the cap so the full stream is available for post-mortem analysis
// of client-side truncation defects (see bead agent-f237e07b).
const envDebugWireStreamFull = "AGENT_DEBUG_WIRE_STREAM_FULL"

func (s *debugSink) emit(ev wireEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	ev.Seq = s.seq
	ev.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	enc := json.NewEncoder(s.w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(ev)
}

// redactHeaders returns a copy of h with any Authorization Bearer token value
// replaced with "Bearer [REDACTED]". The original header map is not modified.
func redactHeaders(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for k, vs := range h {
		cp := make([]string, len(vs))
		for i, v := range vs {
			cp[i] = bearerTokenPattern.ReplaceAllString(v, "${1}[REDACTED]")
		}
		out[k] = cp
	}
	return out
}

// captureBody reads the body fully (up to maxBodyBytes), returns the captured
// bytes plus a replacement io.ReadCloser that replays the full content so the
// downstream reader is unaffected.
func captureBody(body io.ReadCloser) ([]byte, bool, io.ReadCloser, error) {
	if body == nil {
		return nil, false, nil, nil
	}
	buf, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return nil, false, io.NopCloser(bytes.NewReader(buf)), err
	}
	truncated := false
	if len(buf) > maxBodyBytes {
		truncated = true
	}
	return buf, truncated, io.NopCloser(bytes.NewReader(buf)), nil
}

// teeBody wraps a streaming response body so each read is mirrored to the
// sink as a chunk event. A final "response" event is emitted on close with the
// cumulative byte count.
//
// When streamFull is false (default), cumulative captured bytes are capped at
// maxBodyBytes to avoid flooding stderr on long reasoning-model generations.
// Setting AGENT_DEBUG_WIRE_STREAM_FULL=1 disables the cap so the entire
// stream is captured — necessary when diagnosing client-side truncation
// defects (bead agent-f237e07b wire capture stopped at 186 bytes because the
// cap + the teeBody emitting only up to the first cap-sized prefix made
// post-cap frames invisible; this is the fix for step 5 of that bead).
type teeBody struct {
	inner      io.ReadCloser
	sink       *debugSink
	url        string
	status     int
	startTime  time.Time
	total      int
	closed     bool
	streamFull bool
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	if n > 0 {
		t.total += n
		// Capture regime:
		//   streamFull=true  → emit every byte read, no cap.
		//   streamFull=false → emit until cumulative total reaches
		//                      maxBodyBytes, then drop body from events.
		switch {
		case t.streamFull:
			t.sink.emit(wireEvent{
				Dir:       "response",
				URL:       t.url,
				Status:    t.status,
				Body:      string(p[:n]),
				BodyBytes: n,
			})
		case t.total-n < maxBodyBytes:
			chunk := p[:n]
			if t.total > maxBodyBytes {
				chunk = p[:maxBodyBytes-(t.total-n)]
			}
			t.sink.emit(wireEvent{
				Dir:       "response",
				URL:       t.url,
				Status:    t.status,
				Body:      string(chunk),
				BodyBytes: n,
			})
		}
	}
	if err != nil && !t.closed {
		t.closed = true
		ev := wireEvent{
			Dir:       "response",
			URL:       t.url,
			Status:    t.status,
			BodyBytes: t.total,
			LatencyMs: time.Since(t.startTime).Milliseconds(),
		}
		if err != io.EOF {
			ev.Err = err.Error()
		}
		t.sink.emit(ev)
	}
	return n, err
}

func (t *teeBody) Close() error {
	err := t.inner.Close()
	if !t.closed {
		t.closed = true
		t.sink.emit(wireEvent{
			Dir:       "response",
			URL:       t.url,
			Status:    t.status,
			BodyBytes: t.total,
			LatencyMs: time.Since(t.startTime).Milliseconds(),
		})
	}
	return err
}

// debugMiddleware returns an option.Middleware that logs every request and
// response to the shared sink. Called by New() only when the sink is non-nil.
func debugMiddleware(s *debugSink) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		start := time.Now()

		var reqBody []byte
		var reqTruncated bool
		if req.Body != nil {
			var err error
			var newBody io.ReadCloser
			reqBody, reqTruncated, newBody, err = captureBody(req.Body)
			if err == nil {
				req.Body = newBody
			}
		}
		reqBodyStr := string(reqBody)
		if len(reqBody) > maxBodyBytes {
			reqBodyStr = string(reqBody[:maxBodyBytes])
		}
		s.emit(wireEvent{
			Dir:       "request",
			Method:    req.Method,
			URL:       req.URL.String(),
			Headers:   redactHeaders(req.Header),
			BodyBytes: len(reqBody),
			Body:      reqBodyStr,
			Truncated: reqTruncated,
		})

		resp, err := next(req)
		if err != nil {
			s.emit(wireEvent{
				Dir:       "error",
				URL:       req.URL.String(),
				LatencyMs: time.Since(start).Milliseconds(),
				Err:       err.Error(),
			})
			return resp, err
		}

		ct := resp.Header.Get("Content-Type")
		// Stream-shaped responses get per-chunk dump via teeBody. Everything
		// else gets a single response event with the full body captured.
		if strings.Contains(ct, "text/event-stream") {
			resp.Body = &teeBody{
				inner:      resp.Body,
				sink:       s,
				url:        req.URL.String(),
				status:     resp.StatusCode,
				startTime:  start,
				streamFull: envBoolTrue(envDebugWireStreamFull),
			}
			s.emit(wireEvent{
				Dir:        "response",
				URL:        req.URL.String(),
				Status:     resp.StatusCode,
				Headers:    redactHeaders(resp.Header),
				ContentTyp: ct,
				LatencyMs:  time.Since(start).Milliseconds(),
			})
			return resp, nil
		}

		body, truncated, newBody, rerr := captureBody(resp.Body)
		resp.Body = newBody
		bodyStr := string(body)
		if len(body) > maxBodyBytes {
			bodyStr = string(body[:maxBodyBytes])
		}
		ev := wireEvent{
			Dir:        "response",
			URL:        req.URL.String(),
			Status:     resp.StatusCode,
			Headers:    redactHeaders(resp.Header),
			BodyBytes:  len(body),
			Body:       bodyStr,
			Truncated:  truncated,
			LatencyMs:  time.Since(start).Milliseconds(),
			ContentTyp: ct,
		}
		if rerr != nil {
			ev.Err = rerr.Error()
		}
		s.emit(ev)
		return resp, nil
	}
}
