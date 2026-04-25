package agent_test

import (
	"os"
	"strings"
	"testing"
)

// TestContract003_CacheCostProseExists asserts CONTRACT-003 prose explains
// the cache-aware cost attribution semantics introduced by bead D
// (agent-6e2ebcdb): cache tokens are priced at manifest cache rates;
// explicit zero on CacheReadAmount/CacheWriteAmount means the caller opted
// out via CachePolicy=off; nil means the harness or provider did not report.
func TestContract003_CacheCostProseExists(t *testing.T) {
	const path = "docs/helix/02-design/contracts/CONTRACT-003-ddx-agent-service.md"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := strings.ToLower(string(raw))

	required := []string{
		"cost_cache_read_per_m",
		"explicit zero",
		"opted out",
	}
	for _, want := range required {
		if !strings.Contains(body, strings.ToLower(want)) {
			t.Fatalf("CONTRACT-003 missing prose phrase %q (cache-cost semantics)", want)
		}
	}
}
