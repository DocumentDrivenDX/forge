package termbench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Default timeouts match the TerminalBench task.yaml schema defaults
// (https://www.tbench.ai/docs/task-overview). Re-declared here so that
// missing-field tasks still receive sane bounds without needing the
// upstream loader.
const (
	DefaultAgentTimeoutSec = 180
	DefaultTestTimeoutSec  = 30
)

// Task is the typed projection of a TerminalBench task directory we care
// about for adapter purposes. We only read the fields needed to drive
// ServiceExecuteRequest; everything else (Dockerfile, test scripts, etc.)
// stays in the upstream task tree and is the grader's concern.
type Task struct {
	// ID is the task directory name (terminal-bench convention).
	ID string

	// Path is the absolute path to the task directory.
	Path string

	// Instruction is the natural-language prompt the agent receives. Resolved
	// from the descriptions[].instruction matching the chosen difficulty
	// key, falling back to the "base" description.
	Instruction string

	// Difficulty is one of "easy", "medium", "hard" (or empty if not declared).
	Difficulty string

	// Tags is the categorization labels from task.yaml, copied verbatim.
	Tags []string

	// MaxAgentTimeoutSec is the per-task agent wall-clock budget Harbor
	// enforces. The adapter uses this when building ServiceExecuteRequest
	// so the Go-side timeout matches what the grader will tolerate.
	MaxAgentTimeoutSec int

	// MaxTestTimeoutSec is the verifier's pytest timeout. We surface it so
	// reporters can record it; the adapter does not run tests itself.
	MaxTestTimeoutSec int

	// AuthorEmail comes straight from task.yaml; useful for provenance
	// tracking in result artifacts.
	AuthorEmail string
}

// taskYAMLDoc is the on-disk task.yaml shape. The TerminalBench schema
// permits more keys than this; we decode only what the adapter needs.
type taskYAMLDoc struct {
	Descriptions []struct {
		Key         string `yaml:"key"`
		Instruction string `yaml:"instruction"`
	} `yaml:"descriptions"`
	AuthorEmail        string   `yaml:"author_email"`
	Difficulty         string   `yaml:"difficulty"`
	Tags               []string `yaml:"tags"`
	MaxAgentTimeoutSec int      `yaml:"max_agent_timeout_sec"`
	MaxTestTimeoutSec  int      `yaml:"max_test_timeout_sec"`
}

// LoadTask reads a TerminalBench task directory and returns a Task. The
// task ID is inferred from the directory name. Two layouts are supported:
//
//   - TB1 ("terminal-bench"): <taskDir>/task.yaml with descriptions[]
//     embedded plus tests/test_outputs.py. Documented at
//     https://www.tbench.ai/docs/task-overview.
//   - TB2 ("terminal-bench-2"): <taskDir>/task.toml plus a sibling
//     instruction.md and tests/test_outputs.py. Documented at the same
//     URL but the schema is reorganized; our reader extracts only the
//     fields we need so we don't pull in a TOML dependency.
//
// The function does NOT verify Dockerfile/environment presence — those
// are Harbor's concern. We only check the contract surface this adapter
// consumes (instruction text + timeouts).
func LoadTask(taskDir string) (*Task, error) {
	if taskDir == "" {
		return nil, errors.New("termbench: empty task directory")
	}
	abs, err := filepath.Abs(taskDir)
	if err != nil {
		return nil, fmt.Errorf("termbench: resolve task dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(abs, "tests", "test_outputs.py")); err != nil {
		return nil, fmt.Errorf("termbench: task %s missing tests/test_outputs.py: %w", filepath.Base(abs), err)
	}
	if _, err := os.Stat(filepath.Join(abs, "task.toml")); err == nil {
		return loadTaskTOML(abs)
	}
	return loadTaskYAML(abs)
}

func loadTaskYAML(abs string) (*Task, error) {
	yamlPath := filepath.Join(abs, "task.yaml")
	data, err := os.ReadFile(yamlPath) // #nosec G304 -- path is constructed from caller-controlled task dir
	if err != nil {
		return nil, fmt.Errorf("termbench: read task.yaml: %w", err)
	}
	var doc taskYAMLDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("termbench: parse task.yaml: %w", err)
	}
	instruction := pickInstruction(doc.Descriptions)
	if instruction == "" {
		return nil, fmt.Errorf("termbench: task.yaml at %s has no usable description (need 'base' key or non-empty instruction)", yamlPath)
	}
	t := &Task{
		ID:                 filepath.Base(abs),
		Path:               abs,
		Instruction:        instruction,
		Difficulty:         doc.Difficulty,
		Tags:               append([]string(nil), doc.Tags...),
		AuthorEmail:        doc.AuthorEmail,
		MaxAgentTimeoutSec: doc.MaxAgentTimeoutSec,
		MaxTestTimeoutSec:  doc.MaxTestTimeoutSec,
	}
	if t.MaxAgentTimeoutSec <= 0 {
		t.MaxAgentTimeoutSec = DefaultAgentTimeoutSec
	}
	if t.MaxTestTimeoutSec <= 0 {
		t.MaxTestTimeoutSec = DefaultTestTimeoutSec
	}
	return t, nil
}

// loadTaskTOML handles the terminal-bench-2 layout. We do not pull in a
// TOML dependency for this; the schema is stable and the fields we need
// are simple `key = "value"` or `key = N` lines under named sections.
func loadTaskTOML(abs string) (*Task, error) {
	tomlPath := filepath.Join(abs, "task.toml")
	tomlBytes, err := os.ReadFile(tomlPath) // #nosec G304 -- path is constructed from caller-controlled task dir
	if err != nil {
		return nil, fmt.Errorf("termbench: read task.toml: %w", err)
	}
	instructionPath := filepath.Join(abs, "instruction.md")
	instrBytes, err := os.ReadFile(instructionPath) // #nosec G304 -- path is constructed from caller-controlled task dir
	if err != nil {
		return nil, fmt.Errorf("termbench: read instruction.md: %w", err)
	}
	parsed := parseTinyTOML(string(tomlBytes))
	t := &Task{
		ID:          filepath.Base(abs),
		Path:        abs,
		Instruction: string(instrBytes),
		Difficulty:  parsed["metadata.difficulty"],
		AuthorEmail: parsed["metadata.author_email"],
	}
	// Tags is a TOML array; parseTinyTOML returns the raw `[ "a", "b" ]`
	// text — split it deterministically.
	if raw, ok := parsed["metadata.tags"]; ok {
		t.Tags = parseTOMLArray(raw)
	}
	t.MaxAgentTimeoutSec = parseIntOrZero(parsed["agent.timeout_sec"])
	t.MaxTestTimeoutSec = parseIntOrZero(parsed["verifier.timeout_sec"])
	if t.MaxAgentTimeoutSec <= 0 {
		t.MaxAgentTimeoutSec = DefaultAgentTimeoutSec
	}
	if t.MaxTestTimeoutSec <= 0 {
		t.MaxTestTimeoutSec = DefaultTestTimeoutSec
	}
	if strings.TrimSpace(t.Instruction) == "" {
		return nil, fmt.Errorf("termbench: instruction.md at %s is empty", instructionPath)
	}
	return t, nil
}

// parseTinyTOML extracts top-level scalar fields from the named sections
// we care about ("metadata", "agent", "verifier"). It returns a flat map
// keyed by "<section>.<key>". This is intentionally a tiny parser — it
// does not handle nested tables, array-of-tables, multi-line strings, or
// inline tables. The TerminalBench task.toml schema uses only the simple
// scalar/array forms this parser supports; if upstream adds richer
// constructs we want LoadTask to fall through to a real toml library
// rather than silently misparse, so unknown lines are ignored.
func parseTinyTOML(src string) map[string]string {
	out := make(map[string]string)
	section := ""
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.TrimSuffix(val, ",")
		val = strings.Trim(val, "\"")
		full := key
		if section != "" {
			full = section + "." + key
		}
		out[full] = val
	}
	return out
}

func parseTOMLArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "\"")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseIntOrZero(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// task.toml uses floats like "900.0"; trim a trailing ".0".
	if dot := strings.Index(s, "."); dot >= 0 {
		s = s[:dot]
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// pickInstruction returns the instruction string for the "base" key, or
// the first non-empty instruction if no "base" is declared. This mirrors
// the behavior described at https://www.tbench.ai/docs/task-overview where
// "base" is the canonical key and other keys are difficulty-variant.
func pickInstruction(descs []struct {
	Key         string `yaml:"key"`
	Instruction string `yaml:"instruction"`
}) string {
	for _, d := range descs {
		if d.Key == "base" && strings.TrimSpace(d.Instruction) != "" {
			return d.Instruction
		}
	}
	for _, d := range descs {
		if strings.TrimSpace(d.Instruction) != "" {
			return d.Instruction
		}
	}
	return ""
}

// LoadTasks loads all task subdirectories under root that contain a
// task.yaml. Returned slice is sorted by ID for deterministic ordering.
// Non-task directories are silently skipped — TerminalBench's tasks/
// directory may contain README files, schemas, etc., alongside tasks.
func LoadTasks(root string) ([]*Task, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("termbench: read tasks root %s: %w", root, err)
	}
	var tasks []*Task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskDir := filepath.Join(root, entry.Name())
		hasYAML := false
		hasTOML := false
		if _, err := os.Stat(filepath.Join(taskDir, "task.yaml")); err == nil {
			hasYAML = true
		}
		if _, err := os.Stat(filepath.Join(taskDir, "task.toml")); err == nil {
			hasTOML = true
		}
		if !hasYAML && !hasTOML {
			continue
		}
		t, err := LoadTask(taskDir)
		if err != nil {
			// Skip malformed tasks but surface them via wrapped error so
			// callers can decide whether to bail or continue. We accumulate
			// silently here because TerminalBench occasionally lands tasks
			// with placeholder yaml — failing the whole sweep on one bad
			// task is the wrong default.
			continue
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}
