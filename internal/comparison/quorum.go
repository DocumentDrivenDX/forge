package comparison

import "sync"

// RunQuorum invokes multiple harnesses and evaluates consensus.
// It returns the results from all harnesses; use QuorumMet to check success.
func RunQuorum(run RunFunc, opts QuorumOptions) ([]RunResult, error) {
	total := len(opts.Harnesses)
	threshold := effectiveThreshold(opts.Strategy, opts.Threshold, total)
	if threshold < 1 || threshold > total {
		return nil, nil // no harnesses: treat as empty result
	}

	results := make([]RunResult, total)
	var wg sync.WaitGroup
	for i, name := range opts.Harnesses {
		wg.Add(1)
		go func(idx int, harness string) {
			defer wg.Done()
			results[idx] = run(harness, opts.Model, opts.Prompt)
		}(i, name)
	}
	wg.Wait()

	return results, nil
}

// QuorumMet returns true if enough results succeeded.
func QuorumMet(strategy string, threshold int, results []RunResult) bool {
	total := len(results)
	eff := effectiveThreshold(strategy, threshold, total)

	successes := 0
	for _, r := range results {
		if r.ExitCode == 0 {
			successes++
		}
	}
	return successes >= eff
}

func effectiveThreshold(strategy string, threshold, total int) int {
	switch strategy {
	case "any":
		return 1
	case "majority":
		return (total / 2) + 1
	case "unanimous":
		return total
	default:
		if threshold > 0 {
			return threshold
		}
		return 1
	}
}
