package agent

import "regexp"

// overflowPattern matches error messages that indicate the request exceeded the
// model's context window.
var overflowPattern = regexp.MustCompile(
	`(?i)context.?length|token.?limit|maximum.?context|context.?window|` +
		`too.?long|exceeds.*limit|reduce.*length|reduce.*message`,
)

// IsContextOverflowError reports whether err indicates the request exceeded
// the model's context window.
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	return overflowPattern.MatchString(err.Error())
}

// transientPattern matches error messages that indicate a transient condition
// (network blips, rate limits, server overload) that is safe to retry.
var transientPattern = regexp.MustCompile(
	`(?i)overloaded|rate.?limit|too many requests|429|500|502|503|504|` +
		`service.?unavailable|server.?error|internal.?error|` +
		`network.?error|connection.?error|connection.?refused|` +
		`other side closed|fetch failed|socket hang.?up|` +
		`ended without|timed?.?out|timeout|i/o timeout|` +
		`EOF|connection reset|broken pipe`,
)

// fatalPattern matches error messages that indicate a permanent failure
// (authentication/authorization). These should never be retried even if they
// also match a transient pattern.
var fatalPattern = regexp.MustCompile(
	`(?i)401|403|unauthorized|forbidden|invalid.?api.?key|authentication`,
)

// IsTransientError reports whether err is a transient provider error
// that is safe to retry (network issues, rate limits, server overload).
// Returns false for fatal errors (auth failures, bad requests, etc.).
func IsTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if fatalPattern.MatchString(msg) {
		return false
	}
	return transientPattern.MatchString(msg)
}
