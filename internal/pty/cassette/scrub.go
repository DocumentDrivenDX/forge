package cassette

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Scrubber struct {
	Home                   string
	Worktree               string
	EnvAllowlist           []string
	AccountIdentifiers     []string
	IntentionallyPreserved []string
	rules                  []scrubRule
}

type scrubRule struct {
	name        string
	re          *regexp.Regexp
	replacement string
}

func NewScrubber(cfg Scrubber) *Scrubber {
	s := cfg
	s.rules = []scrubRule{
		{name: "bearer-token", re: regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`), replacement: "Bearer <redacted>"},
		{name: "api-token", re: regexp.MustCompile(`(?i)(api[_-]?key|token|secret)=?[A-Za-z0-9._~+/=-]{8,}`), replacement: "$1=<redacted>"},
		{name: "email", re: regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`), replacement: "<account>"},
		{name: "uuid", re: regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`), replacement: "<uuid>"},
		{name: "rfc3339", re: regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z?`), replacement: "<timestamp>"},
		{name: "local-time", re: regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}\b`), replacement: "<time>"},
		{name: "pid", re: regexp.MustCompile(`\b(pid|PID)[=: ]+\d+\b`), replacement: "$1=<pid>"},
		{name: "elapsed-duration", re: regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|s|m|h)\b`), replacement: "<duration>"},
		{name: "socket-file", re: regexp.MustCompile(`/tmp/[A-Za-z0-9._/-]*\.sock\b`), replacement: "<socket>"},
		{name: "transient-file", re: regexp.MustCompile(`/tmp/[A-Za-z0-9._/-]*(?:tmp|temp)[A-Za-z0-9._/-]*`), replacement: "<tmpfile>"},
		{name: "animation-counter", re: regexp.MustCompile(`\b(frame|tick|counter)=\d+\b`), replacement: "$1=<n>"},
	}
	return &s
}

func (s *Scrubber) ScrubString(input string) (string, ScrubReport) {
	if s == nil {
		s = NewScrubber(Scrubber{})
	}
	out := input
	report := ScrubReport{Status: "clean", HitCounts: map[string]int{}, EnvAllowlist: append([]string(nil), s.EnvAllowlist...), IntentionallyPreserved: append([]string(nil), s.IntentionallyPreserved...)}
	if s.Home != "" {
		out = strings.ReplaceAll(out, filepath.Clean(s.Home), "$HOME")
	}
	if s.Worktree != "" {
		out = strings.ReplaceAll(out, filepath.Clean(s.Worktree), "$WORKTREE")
	}
	for _, account := range s.AccountIdentifiers {
		if account == "" {
			continue
		}
		count := strings.Count(out, account)
		if count > 0 {
			report.HitCounts["account-identifier"] += count
			out = strings.ReplaceAll(out, account, "<account>")
		}
	}
	for _, rule := range s.rules {
		matches := rule.re.FindAllStringIndex(out, -1)
		if len(matches) == 0 {
			continue
		}
		report.HitCounts[rule.name] += len(matches)
		out = rule.re.ReplaceAllString(out, rule.replacement)
	}
	for _, rule := range s.ruleNames() {
		report.Rules = append(report.Rules, rule)
	}
	if len(report.HitCounts) > 0 {
		report.Status = "redacted"
	}
	return out, report
}

func (s *Scrubber) ScrubEnv(env map[string]string) (map[string]string, ScrubReport) {
	allow := map[string]bool{}
	for _, name := range s.EnvAllowlist {
		allow[name] = true
	}
	out := map[string]string{}
	report := ScrubReport{Status: "clean", Rules: s.ruleNames(), EnvAllowlist: append([]string(nil), s.EnvAllowlist...), HitCounts: map[string]int{}}
	for k, v := range env {
		if allow[k] {
			out[k] = v
			continue
		}
		report.HitCounts["env-removed"]++
	}
	if report.HitCounts["env-removed"] > 0 {
		report.Status = "redacted"
	}
	return out, report
}

func (s *Scrubber) NormalizeVolatile(input string) string {
	out, _ := s.ScrubString(input)
	return out
}

func (s *Scrubber) ruleNames() []string {
	names := make([]string, 0, len(s.rules)+2)
	if s.Home != "" {
		names = append(names, "home-path")
	}
	if s.Worktree != "" {
		names = append(names, "worktree-path")
	}
	for _, rule := range s.rules {
		names = append(names, rule.name)
	}
	sort.Strings(names)
	return names
}
