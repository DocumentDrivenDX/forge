package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// CorpusTask is a single benchmark task loaded from a YAML or JSON file in the
// corpus directory.
type CorpusTask struct {
	ID            string   `yaml:"id"            json:"id"`
	Description   string   `yaml:"description"   json:"description"`
	Prompt        string   `yaml:"prompt"        json:"prompt"`
	ExpectedTools []string `yaml:"expected_tools" json:"expected_tools,omitempty"`
	Permissions   string   `yaml:"permissions"   json:"permissions,omitempty"`
	Reasoning     string   `yaml:"reasoning"     json:"reasoning,omitempty"`
	Tags          []string `yaml:"tags"          json:"tags,omitempty"`
}

// loadCorpus reads all *.yaml / *.yml / *.json files from dir and returns
// the parsed tasks in filename order.
func loadCorpus(dir string) ([]CorpusTask, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read corpus dir %s: %w", dir, err)
	}

	var tasks []CorpusTask
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") &&
			!strings.HasSuffix(name, ".yml") &&
			!strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var t CorpusTask
		if strings.HasSuffix(name, ".json") {
			if err := json.Unmarshal(data, &t); err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
		} else {
			if err := yaml.Unmarshal(data, &t); err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
		}
		if t.ID == "" {
			t.ID = strings.TrimSuffix(name, filepath.Ext(name))
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}
