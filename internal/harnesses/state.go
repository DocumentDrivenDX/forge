package harnesses

import (
	"regexp"
	"strconv"
	"strings"
)

// quotaPattern matches lines like:
//
//	"83% of 5h limit"
//	"75% of 7 day limit, resets April 12"
//	"83% of 5h limit (resets April 12)"
var quotaPattern = regexp.MustCompile(
	`(\d+)%\s+of\s+([\w\s]+?)\s+limit(?:[,\s]+resets?\s+([\w\s]+\d+))?`,
)

// ParseQuotaOutput parses the text output of a harness quota command.
// It extracts percent_used, limit_window, and reset_date.
// Returns nil if no quota data is found.
func ParseQuotaOutput(output string) *QuotaInfo {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := quotaPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pct, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		info := &QuotaInfo{
			PercentUsed: pct,
			LimitWindow: strings.TrimSpace(m[2]),
		}
		if m[3] != "" {
			info.ResetDate = strings.TrimSpace(m[3])
		}
		return info
	}
	return nil
}
