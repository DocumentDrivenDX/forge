package openai

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/option"
)

// The openai-go ssestream decoder (packages/ssestream/ssestream.go) dispatches
// an SSE event on any blank line, regardless of whether the preceding frame
// contained any `data:` fields. When an OpenAI-compatible server emits a
// keep-alive comment frame like:
//
//	: keep-alive
//	(blank)
//
// the decoder dispatches an event with empty Data. Stream.Next then attempts
// `json.Unmarshal([]byte{}, &chunk)` and surfaces "unexpected end of JSON
// input", aborting the stream.
//
// Per the SSE spec (WHATWG HTML §server-sent-events), events with an empty
// data buffer must be silently ignored. Until upstream openai-go is patched,
// we filter the response body before it reaches the SDK's decoder: we strip
// comment lines AND suppress any blank line that would otherwise dispatch an
// event carrying no `data:` fields.
//
// Bead: agent-f237e07b. Wire evidence: vidar-omlx keep-alive frames during
// Qwen3 reasoning warmup.
//
// Upstream tracking (as of 2026-04-18, openai-go v1.12.0 — latest):
//   - Issue openai/openai-go#556 — "SSE Stream Crashes on Empty Events"
//   - Issue openai/openai-go#618 — "eventStreamDecoder can emit incomplete JSON"
//   - PR openai/openai-go#555   — first proposed fix (open, stalled)
//   - PR openai/openai-go#643   — proposed fix matching this analysis (open)
//
// Remove this filter and the middleware wiring in openai.go once one of those
// PRs merges and we bump the openai-go dependency to the release containing it.

// sseCommentFilter wraps an SSE response body, stripping comment-only frames
// so they do not cause empty-event dispatches in the downstream decoder.
type sseCommentFilter struct {
	src     *bufio.Reader
	inner   io.Closer
	pending []byte
	// frameHasData tracks whether the current (in-progress) event has seen at
	// least one non-comment field line. A blank line dispatches iff true; when
	// false, the blank line is swallowed so the decoder never sees a
	// comment-only event.
	frameHasData bool
	atEOF        bool
}

func newSSECommentFilter(body io.ReadCloser) io.ReadCloser {
	return &sseCommentFilter{
		src:   bufio.NewReader(body),
		inner: body,
	}
}

func (f *sseCommentFilter) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Drain any pending cleaned bytes first.
	if len(f.pending) > 0 {
		n := copy(p, f.pending)
		f.pending = f.pending[n:]
		return n, nil
	}
	if f.atEOF {
		return 0, io.EOF
	}

	// Accumulate cleaned output until we fill p or hit EOF.
	var out []byte
	for len(out) < len(p) {
		line, err := f.src.ReadBytes('\n')
		if len(line) > 0 {
			cleaned := f.classify(line)
			if cleaned != nil {
				out = append(out, cleaned...)
			}
		}
		if err != nil {
			if err == io.EOF {
				f.atEOF = true
				break
			}
			// On other errors, stash what we have and return the error on the
			// next call.
			if len(out) > 0 {
				f.pending = out
				return 0, nil
			}
			return 0, err
		}
	}

	if len(out) == 0 && f.atEOF {
		return 0, io.EOF
	}

	n := copy(p, out)
	if n < len(out) {
		f.pending = out[n:]
	}
	return n, nil
}

// classify returns the cleaned bytes that should be forwarded for the given
// raw upstream line (terminator included). nil means "swallow this line".
//
// Frame semantics:
//   - Lines starting with `:` are SSE comments — always swallow.
//   - Blank lines dispatch the pending event. Swallow iff the current frame
//     has not seen any field line yet (would dispatch an empty event).
//   - All other lines are field lines (data:, event:, id:, retry:, or a name
//     without a colon). Forward them and mark the frame as having data.
func (f *sseCommentFilter) classify(line []byte) []byte {
	trimmed := bytes.TrimRight(line, "\r\n")
	if len(trimmed) == 0 {
		if !f.frameHasData {
			return nil
		}
		f.frameHasData = false
		return line
	}
	if trimmed[0] == ':' {
		return nil
	}
	f.frameHasData = true
	return line
}

func (f *sseCommentFilter) Close() error {
	return f.inner.Close()
}

// sseFilterMiddleware returns an option.Middleware that wraps streaming
// responses with sseCommentFilter so the downstream ssestream decoder never
// sees comment-only event dispatches.
func sseFilterMiddleware() option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		resp, err := next(req)
		if err != nil || resp == nil || resp.Body == nil {
			return resp, err
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/event-stream") {
			return resp, nil
		}
		resp.Body = newSSECommentFilter(resp.Body)
		return resp, nil
	}
}
