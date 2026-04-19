package observations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/session"
)

// PopulateFromLogs reads all JSONL session files in logDir and adds
// speed observations to the store for successful sessions.
func PopulateFromLogs(store *Store, logDir string) error {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("observations: read log dir %s: %w", logDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		path := filepath.Join(logDir, name)
		if err := processLogFile(store, path); err != nil {
			// Skip files that fail to parse; best-effort.
			continue
		}
	}
	return nil
}

func processLogFile(store *Store, path string) error {
	events, err := session.ReadEvents(path)
	if err != nil {
		return err
	}

	for _, e := range events {
		if e.Type != agent.EventSessionEnd {
			continue
		}
		var data session.SessionEndData
		if err := json.Unmarshal(e.Data, &data); err != nil {
			continue
		}
		if data.Status != agent.StatusSuccess {
			continue
		}
		if data.DurationMs <= 0 || data.Tokens.Output <= 0 {
			continue
		}

		outputTokensPerSec := float64(data.Tokens.Output) / (float64(data.DurationMs) / 1000)
		providerSystem := data.SelectedProvider
		model := data.ResolvedModel
		if model == "" {
			model = data.Model
		}
		if providerSystem == "" || model == "" {
			continue
		}

		store.Add(Key{
			ProviderSystem: providerSystem,
			Model:          model,
		}, Sample{
			OutputTokensPerSec: outputTokensPerSec,
			DurationMs:         data.DurationMs,
			RecordedAt:         e.Timestamp,
		})
	}
	return nil
}
