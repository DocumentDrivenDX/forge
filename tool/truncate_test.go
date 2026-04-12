package tool

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncateHead_UnderLimits(t *testing.T) {
	s := "line1\nline2\nline3"
	result := TruncateHead(s, 2000, 51200)
	assert.Equal(t, s, result, "content under both limits should pass through unchanged")
}

func TestTruncateHead_Empty(t *testing.T) {
	result := TruncateHead("", 2000, 51200)
	assert.Equal(t, "", result, "empty input should return empty string")
}

func TestTruncateHead_OverLineLimit(t *testing.T) {
	lines := make([]string, 2100)
	for i := range lines {
		lines[i] = "line"
	}
	s := strings.Join(lines, "\n")
	result := TruncateHead(s, 2000, 51200)
	assert.Contains(t, result, "[Truncated: 100 lines omitted]")
	resultLines := strings.Split(result, "\n")
	// 2000 content lines + 1 marker line
	assert.Equal(t, 2001, len(resultLines))
	assert.Equal(t, resultLines[0], "line")
	assert.Equal(t, resultLines[1999], "line")
}

func TestTruncateHead_OverByteLimit(t *testing.T) {
	// Each line is 100 bytes + newline = 101 bytes; 600 lines = ~60.6 KB > 50 KB
	line := strings.Repeat("x", 100)
	lines := make([]string, 600)
	for i := range lines {
		lines[i] = line
	}
	s := strings.Join(lines, "\n")
	result := TruncateHead(s, 2000, 51200)
	assert.Contains(t, result, "[Truncated:")
	assert.Contains(t, result, "lines omitted]")
	// Result should be well under 51200 bytes (marker is small)
	assert.Less(t, len(result), 52000)
}

func TestTruncateHead_HeadBehavior(t *testing.T) {
	// Head: first lines are kept, last lines are dropped
	lines := make([]string, 2100)
	for i := range lines {
		if i < 2000 {
			lines[i] = "keep"
		} else {
			lines[i] = "drop"
		}
	}
	s := strings.Join(lines, "\n")
	result := TruncateHead(s, 2000, 51200)
	assert.NotContains(t, result[:len(result)-50], "drop", "dropped lines should not appear in kept content")
	assert.Contains(t, result, "keep")
}

func TestTruncateTail_UnderLimits(t *testing.T) {
	s := "line1\nline2\nline3"
	result := TruncateTail(s, 2000, 51200)
	assert.Equal(t, s, result, "content under both limits should pass through unchanged")
}

func TestTruncateTail_Empty(t *testing.T) {
	result := TruncateTail("", 2000, 51200)
	assert.Equal(t, "", result, "empty input should return empty string")
}

func TestTruncateTail_OverLineLimit(t *testing.T) {
	lines := make([]string, 2100)
	for i := range lines {
		lines[i] = "line"
	}
	s := strings.Join(lines, "\n")
	result := TruncateTail(s, 2000, 51200)
	assert.Contains(t, result, "[Truncated: 100 lines omitted]")
	resultLines := strings.Split(result, "\n")
	// 1 marker line + 2000 content lines
	assert.Equal(t, 2001, len(resultLines))
	assert.Equal(t, resultLines[1], "line")
	assert.Equal(t, resultLines[2000], "line")
}

func TestTruncateTail_OverByteLimit(t *testing.T) {
	line := strings.Repeat("x", 100)
	lines := make([]string, 600)
	for i := range lines {
		lines[i] = line
	}
	s := strings.Join(lines, "\n")
	result := TruncateTail(s, 2000, 51200)
	assert.Contains(t, result, "[Truncated:")
	assert.Contains(t, result, "lines omitted]")
	assert.Less(t, len(result), 52000)
}

func TestTruncateTail_TailBehavior(t *testing.T) {
	// Tail: last lines are kept, first lines are dropped
	lines := make([]string, 2100)
	for i := range lines {
		if i < 100 {
			lines[i] = "drop"
		} else {
			lines[i] = "keep"
		}
	}
	s := strings.Join(lines, "\n")
	result := TruncateTail(s, 2000, 51200)
	// The marker is at the top; "drop" lines are gone from the content portion
	markerEnd := strings.Index(result, "\n")
	contentAfterMarker := result[markerEnd+1:]
	assert.NotContains(t, contentAfterMarker, "drop")
	assert.Contains(t, contentAfterMarker, "keep")
}
