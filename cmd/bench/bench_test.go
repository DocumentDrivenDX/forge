package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadCorpus verifies that the bundled corpus files parse without error.
func TestLoadCorpus(t *testing.T) {
	// Walk up from the test binary location to find bench/corpus.
	// The test is run from the package directory, so we go two levels up.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// cmd/bench -> repo root -> bench/corpus
	repoRoot := filepath.Join(wd, "..", "..")
	corpusDir := filepath.Join(repoRoot, "bench", "corpus")

	if _, err := os.Stat(corpusDir); os.IsNotExist(err) {
		t.Skipf("corpus directory not found at %s — skipping", corpusDir)
	}

	tasks, err := loadCorpus(corpusDir)
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected at least one corpus task, got 0")
	}
	for _, task := range tasks {
		if task.ID == "" {
			t.Errorf("task %+v: missing id", task)
		}
		if task.Prompt == "" {
			t.Errorf("task %s: missing prompt", task.ID)
		}
	}
	t.Logf("loaded %d corpus tasks", len(tasks))
}

// TestDiscoverSmoke runs discovery against a minimal (possibly empty) config.
// It verifies the path doesn't panic; empty results are fine in CI.
func TestDiscoverSmoke(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Resolve to repo root where config may live.
	repoRoot := filepath.Join(wd, "..", "..")

	candidates, err := discoverCandidates(repoRoot)
	if err != nil {
		// Config not found is acceptable in a clean CI environment.
		t.Logf("discoverCandidates returned error (acceptable in CI): %v", err)
		return
	}
	t.Logf("discovered %d candidates", len(candidates))
	// Just verify the shape — each candidate must have a harness name.
	for _, c := range candidates {
		if c.Harness == "" {
			t.Errorf("candidate with empty harness: %+v", c)
		}
	}
}

// TestBuildSuite verifies that buildSuite produces a non-nil suite with
// arms and prompts populated from the corpus.
func TestBuildSuite(t *testing.T) {
	tasks := []CorpusTask{
		{ID: "t1", Description: "task one", Prompt: "do task one"},
		{ID: "t2", Description: "task two", Prompt: "do task two"},
	}
	candidates := []Candidate{
		{Harness: "claude", Available: true},
		{Harness: "codex", Available: true},
	}

	suite := buildSuite(tasks, candidates)
	if suite == nil {
		t.Fatal("buildSuite returned nil")
	}
	if len(suite.Arms) != 2 {
		t.Errorf("expected 2 arms, got %d", len(suite.Arms))
	}
	if len(suite.Prompts) != 2 {
		t.Errorf("expected 2 prompts, got %d", len(suite.Prompts))
	}
}
