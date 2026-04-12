package tool

import (
	"fmt"
	"strings"
)

const (
	truncMaxLines = 2000
	truncMaxBytes = 51200 // 50 KB
)

// TruncateHead keeps the first maxLines lines or maxBytes bytes, whichever hits
// first. If the content was cut, a truncation marker is appended.
func TruncateHead(s string, maxLines int, maxBytes int) string {
	if s == "" {
		return s
	}

	lines := strings.Split(s, "\n")
	total := len(lines)

	kept := 0
	size := 0
	for _, line := range lines {
		// +1 for the newline that was between lines
		lineSize := len(line) + 1
		if kept >= maxLines || size+lineSize > maxBytes {
			break
		}
		kept++
		size += lineSize
	}

	if kept >= total {
		return s
	}

	omitted := total - kept
	joined := strings.Join(lines[:kept], "\n")
	return fmt.Sprintf("%s\n[Truncated: %d lines omitted]", joined, omitted)
}

// TruncateTail keeps the last maxLines lines or maxBytes bytes, whichever hits
// first. If the content was cut, a truncation marker is prepended.
func TruncateTail(s string, maxLines int, maxBytes int) string {
	if s == "" {
		return s
	}

	lines := strings.Split(s, "\n")
	total := len(lines)

	kept := 0
	size := 0
	for i := total - 1; i >= 0; i-- {
		lineSize := len(lines[i]) + 1
		if kept >= maxLines || size+lineSize > maxBytes {
			break
		}
		kept++
		size += lineSize
	}

	if kept >= total {
		return s
	}

	omitted := total - kept
	joined := strings.Join(lines[total-kept:], "\n")
	return fmt.Sprintf("[Truncated: %d lines omitted]\n%s", omitted, joined)
}
