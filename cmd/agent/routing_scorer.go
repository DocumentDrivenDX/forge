package main

// CandidateScorer allows external callers to overlay scores on top of the
// smart routing composite score. DDx uses this to factor in quota availability
// without forking the routing logic.
//
// Score returns an adjusted score for a candidate. The baseScore is the
// composite score already computed from reliability, performance, load, cost,
// and capability. The scorer may return the baseScore unchanged, clamp it to
// zero (e.g., quota exhausted), or boost it (e.g., priority quota).
//
// If the scorer returns a negative value, the candidate is treated as
// unhealthy and excluded from the ordering.
type CandidateScorer interface {
	Score(provider, model string, baseScore float64) float64
}

// CandidateScorerFunc is a func adapter for CandidateScorer.
type CandidateScorerFunc func(provider, model string, baseScore float64) float64

func (f CandidateScorerFunc) Score(provider, model string, baseScore float64) float64 {
	return f(provider, model, baseScore)
}
