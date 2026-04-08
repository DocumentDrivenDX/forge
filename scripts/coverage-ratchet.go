// Package main provides the coverage ratchet enforcement tool.
// It checks that test coverage meets committed floor values.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FloorConfig represents the coverage floor configuration.
type FloorConfig struct {
	Version    int               `json:"version"`
	Metric     string            `json:"metric"`
	Updated    string            `json:"updated"`
	UpdatedBy  string            `json:"updated_by"`
	Commit     string            `json:"commit"`
	Floors     map[string]float64 `json:"floors"`
	Tolerances map[string]float64 `json:"tolerances"`
	History    []HistoryEntry   `json:"history"`
}

// HistoryEntry records a floor change.
type HistoryEntry struct {
	Date   string `json:"date"`
	Commit string `json:"commit"`
	Action string `json:"action"`
	Note   string `json:"note"`
}

// PackageCoverage represents coverage for a single package.
type PackageCoverage struct {
	Package   string
	Coverage  float64
	Floor     float64
	Tolerance float64
	Delta     float64
	Pass      bool
}

func main() {
	os.Exit(run())
}

func run() int {
	// Check for bump flag
	for _, arg := range os.Args[1:] {
		if arg == "--bump" {
			if err := bumpFloors(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return 2
			}
			return 0
		}
	}

	// Find project root
	projectRoot, err := findProjectRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 2
	}

	// Load floor config
	floorPath := filepath.Join(projectRoot, ".helix-ratchets", "coverage-floor.json")
	floors, err := loadFloorConfig(floorPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading floor config: %v\n", err)
		return 2
	}

	// Run coverage measurement
	coverages, err := measureCoverage(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error measuring coverage: %v\n", err)
		return 2
	}

	// Check each package against its floor
	var results []PackageCoverage
	var failures []string
	var autoBumps []string

	for _, cov := range coverages {
		floor := getFloor(floors, cov.Package)
		tolerance := getTolerance(floors, cov.Package)

		result := PackageCoverage{
			Package:   cov.Package,
			Coverage:  cov.Coverage,
			Floor:     floor,
			Tolerance: tolerance,
			Delta:     cov.Coverage - floor,
			Pass:      cov.Coverage >= floor-tolerance,
		}
		results = append(results, result)

		if !result.Pass {
			failures = append(failures, fmt.Sprintf("  %s: %.1f%% (floor: %.1f%%, tolerance: %.1f%%)",
				cov.Package, cov.Coverage, floor, tolerance))
		}

		// Check for auto-bump opportunity (coverage exceeds floor by >10%)
		if cov.Coverage >= floor+10 {
			autoBumps = append(autoBumps, fmt.Sprintf("  %s: %.1f%% -> %.1f%% (auto-bump eligible)",
				cov.Package, floor, cov.Coverage-5))
		}
	}

	// Print report
	fmt.Println("Coverage Ratchet Report")
	fmt.Println("========================")
	fmt.Println()

	// Overall summary
	passCount := len(results) - len(failures)
	totalCount := len(results)
	fmt.Printf("Packages: %d/%d passing\n", passCount, totalCount)
	fmt.Println()

	if len(failures) > 0 {
		fmt.Println("FAILURES (below floor - tolerance):")
		for _, f := range failures {
			fmt.Println(f)
		}
		fmt.Println()
	}

	// Detailed table
	fmt.Printf("%-50s %8s %8s %8s %8s\n", "Package", "Cover", "Floor", "Delta", "Status")
	fmt.Printf("%-50s %8s %8s %8s %8s\n", strings.Repeat("-", 50), strings.Repeat("-", 8), strings.Repeat("-", 8), strings.Repeat("-", 8), strings.Repeat("-", 8))
	for _, r := range results {
		status := "PASS"
		if !r.Pass {
			status = "FAIL"
		}
		fmt.Printf("%-50s %7.1f%% %7.1f%% %+7.1f%% %s\n", r.Package, r.Coverage, r.Floor, r.Delta, status)
	}

	// Auto-bump recommendations
	if len(autoBumps) > 0 {
		fmt.Println()
		fmt.Println("Auto-bump eligible (coverage exceeds floor by >10%):")
		for _, b := range autoBumps {
			fmt.Println(b)
		}
	}

	fmt.Println()

	// Return code
	if len(failures) > 0 {
		fmt.Println("RESULT: FAIL - One or more packages below floor")
		return 1
	}

	fmt.Println("RESULT: PASS - All packages meet or exceed floor")

	// Auto-bump if eligible
	if len(autoBumps) > 0 {
		fmt.Println()
		fmt.Println("To auto-bump floors, run: make coverage-bump")
	}

	return 0
}

func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, ".helix-ratchets")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("project root not found")
		}
		dir = parent
	}
}

func loadFloorConfig(path string) (*FloorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg FloorConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func measureCoverage(projectRoot string) ([]PackageCoverage, error) {
	// Run go test -cover to get package coverage
	cmd := exec.Command("go", "test", "-cover", "./...")
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Might still have output
		fmt.Fprintf(os.Stderr, "go test: %s\n", string(output))
	}

	var results []PackageCoverage
	lines := strings.Split(string(output), "\n")

	// Parse lines like: ok  	github.com/DocumentDrivenDX/agent	0.002s	coverage: 82.1% of statements
	// Or: 	github.com/DocumentDrivenDX/agent/cmd/ddx-agent		coverage: 0.0% of statements
	re := regexp.MustCompile(`(?:ok|FAIL|\?)\s+([^\s]+).*?coverage:\s*([0-9.]+)%`)

	for _, line := range lines {
		matches := re.FindStringSubmatch(line)
		if len(matches) >= 3 {
			pkg := matches[1]
			cov, _ := strconv.ParseFloat(matches[2], 64)

			// Skip internal scripts package
			if strings.Contains(pkg, "/scripts") {
				continue
			}

			results = append(results, PackageCoverage{
				Package:  pkg,
				Coverage: cov,
			})
		}
	}

	return results, nil
}

func getFloor(cfg *FloorConfig, pkg string) float64 {
	// Check exact match
	if floor, ok := cfg.Floors[pkg]; ok {
		return floor
	}

	// Check partial matches (longest prefix wins)
	parts := strings.Split(pkg, "/")
	for i := len(parts); i > 0; i-- {
		prefix := strings.Join(parts[:i], "/")
		if floor, ok := cfg.Floors[prefix]; ok {
			return floor
		}
	}

	// Default floor for packages not in config
	return 50.0
}

func getTolerance(cfg *FloorConfig, pkg string) float64 {
	if tol, ok := cfg.Tolerances[pkg]; ok {
		return tol
	}
	if tol, ok := cfg.Tolerances["default"]; ok {
		return tol
	}
	return 2.0
}

func bumpFloors() error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return err
	}

	floorPath := filepath.Join(projectRoot, ".helix-ratchets", "coverage-floor.json")
	floors, err := loadFloorConfig(floorPath)
	if err != nil {
		return err
	}

	// Measure coverage
	coverages, err := measureCoverage(projectRoot)
	if err != nil {
		return err
	}

	// Update floors where coverage exceeds floor by >10%
	changed := false
	for _, cov := range coverages {
		currentFloor := getFloor(floors, cov.Package)
		if cov.Coverage >= currentFloor+10 && cov.Coverage > currentFloor+5 {
			floors.Floors[cov.Package] = cov.Coverage - 5 // Leave 5% buffer
			changed = true
			fmt.Printf("Auto-bump: %s: %.1f%% -> %.1f%%\n", cov.Package, currentFloor, floors.Floors[cov.Package])
		}
	}

	if !changed {
		fmt.Println("No floors eligible for auto-bump")
		return nil
	}

	// Update metadata
	floors.Updated = time.Now().Format(time.RFC3339)
	floors.Commit = getCurrentCommit(projectRoot)
	floors.History = append(floors.History, HistoryEntry{
		Date:   time.Now().Format("2006-01-02"),
		Commit: floors.Commit,
		Action: "auto-bump",
		Note:   "coverage exceeded floor by >10%",
	})

	// Write updated config
	data, err := json.MarshalIndent(floors, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(floorPath, data, 0644)
}

func getCurrentCommit(projectRoot string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = projectRoot
	output, _ := cmd.Output()
	return strings.TrimSpace(string(output))
}
