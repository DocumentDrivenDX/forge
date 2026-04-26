package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// floatEq compares two float metrics with a tolerance suitable for the
// rates in this package (denominators are in the hundreds at most).
func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// makeOverride is a tiny helper for building synthetic ServiceOverrideData
// payloads in tests. axes is the list of overridden axes; matches names
// the subset of those axes that match auto. promptBucket determines the
// prompt-feature inputs (only EstimatedTokens / RequiresTools / Reasoning).
// outcomeStatus stamps a synthetic outcome (empty string = no outcome).
func makeOverride(axes []string, matches []string, estimatedTokens int, requiresTools bool, reasoning string, outcomeStatus string) ServiceOverrideData {
	mpa := make(map[string]bool, len(axes))
	for _, a := range axes {
		mpa[a] = false
	}
	for _, m := range matches {
		mpa[m] = true
	}
	pf := ServiceOverridePromptFeatures{
		RequiresTools: requiresTools,
		Reasoning:     reasoning,
	}
	if estimatedTokens > 0 {
		t := estimatedTokens
		pf.EstimatedTokens = &t
	}
	ov := ServiceOverrideData{
		AxesOverridden: append([]string(nil), axes...),
		MatchPerAxis:   mpa,
		PromptFeatures: pf,
	}
	if outcomeStatus != "" {
		ov.Outcome = &ServiceOverrideOutcome{Status: outcomeStatus}
	}
	return ov
}

// AC #4
func TestRoutingQualityMetricsAcceptanceRate(t *testing.T) {
	overrides := make([]ServiceOverrideData, 0, 30)
	for i := 0; i < 30; i++ {
		overrides = append(overrides, makeOverride(
			[]string{"model"}, nil, 0, false, "", "",
		))
	}
	m := computeRoutingQualityMetrics(100, overrides)
	if !floatEq(m.AutoAcceptanceRate, 0.70) {
		t.Fatalf("AutoAcceptanceRate = %v, want 0.70", m.AutoAcceptanceRate)
	}
	if m.TotalRequests != 100 {
		t.Fatalf("TotalRequests = %d, want 100", m.TotalRequests)
	}
	if m.TotalOverrides != 30 {
		t.Fatalf("TotalOverrides = %d, want 30", m.TotalOverrides)
	}
}

// AC #5
func TestRoutingQualityMetricsDisagreementRate(t *testing.T) {
	overrides := make([]ServiceOverrideData, 0, 30)
	// 18 overrides where pin != auto on the overridden axis.
	for i := 0; i < 18; i++ {
		overrides = append(overrides, makeOverride(
			[]string{"model"}, nil /* no matching axes */, 0, false, "", "",
		))
	}
	// 12 coincidental-agreement overrides — pin matches auto on the
	// overridden axis. They are in the denominator but not the numerator.
	for i := 0; i < 12; i++ {
		overrides = append(overrides, makeOverride(
			[]string{"model"}, []string{"model"}, 0, false, "", "",
		))
	}
	m := computeRoutingQualityMetrics(30, overrides)
	if !floatEq(m.OverrideDisagreementRate, 0.60) {
		t.Fatalf("OverrideDisagreementRate = %v, want 0.60", m.OverrideDisagreementRate)
	}
	// Coincidental-agreement overrides count as overrides (they pull the
	// rate down, which is the headline behavior of this metric).
	if m.TotalOverrides != 30 {
		t.Fatalf("TotalOverrides = %d, want 30", m.TotalOverrides)
	}
	// AutoAcceptanceRate should be 0 because every request was overridden.
	if !floatEq(m.AutoAcceptanceRate, 0.0) {
		t.Fatalf("AutoAcceptanceRate = %v, want 0.0", m.AutoAcceptanceRate)
	}
}

// AC #6
func TestRoutingQualityMetricsClassBreakdown(t *testing.T) {
	// Three distinct prompt feature buckets × axes × outcomes.
	// (a) tokens=small,tools=yes,reasoning=high, axis=harness, mismatch, success x2
	// (b) tokens=small,tools=yes,reasoning=high, axis=model, match, stalled x1
	// (c) tokens=large,tools=no,reasoning=none, axis=provider, mismatch, failed x1
	// (d) tokens=small,tools=yes,reasoning=high, axis=harness, mismatch, failed x1
	overrides := []ServiceOverrideData{
		makeOverride([]string{"harness"}, nil, 2000, true, "high", "success"),
		makeOverride([]string{"harness"}, nil, 2000, true, "high", "success"),
		makeOverride([]string{"model"}, []string{"model"}, 2000, true, "high", "stalled"),
		makeOverride([]string{"provider"}, nil, 50000, false, "", "failed"),
		makeOverride([]string{"harness"}, nil, 2000, true, "high", "failed"),
	}
	m := computeRoutingQualityMetrics(5, overrides)

	if len(m.OverrideClassBreakdown) != 3 {
		t.Fatalf("breakdown len = %d, want 3 (rows: %+v)", len(m.OverrideClassBreakdown), m.OverrideClassBreakdown)
	}

	// Find the harness-mismatch / small-tokens-tools-high bucket.
	var harnessBucket *OverrideClassBucket
	var modelBucket *OverrideClassBucket
	var providerBucket *OverrideClassBucket
	for i := range m.OverrideClassBreakdown {
		b := &m.OverrideClassBreakdown[i]
		switch {
		case b.Axis == "harness" && !b.Match && strings.Contains(b.PromptFeatureBucket, "tokens=small"):
			harnessBucket = b
		case b.Axis == "model" && b.Match:
			modelBucket = b
		case b.Axis == "provider" && !b.Match && strings.Contains(b.PromptFeatureBucket, "tokens=large"):
			providerBucket = b
		}
	}
	if harnessBucket == nil {
		t.Fatalf("missing harness mismatch bucket; got %+v", m.OverrideClassBreakdown)
	}
	if harnessBucket.Count != 3 {
		t.Errorf("harness bucket Count = %d, want 3", harnessBucket.Count)
	}
	if harnessBucket.SuccessOutcomes != 2 {
		t.Errorf("harness bucket SuccessOutcomes = %d, want 2", harnessBucket.SuccessOutcomes)
	}
	if harnessBucket.FailedOutcomes != 1 {
		t.Errorf("harness bucket FailedOutcomes = %d, want 1", harnessBucket.FailedOutcomes)
	}
	if modelBucket == nil || modelBucket.Count != 1 || modelBucket.StalledOutcomes != 1 {
		t.Errorf("model bucket wrong: %+v", modelBucket)
	}
	if providerBucket == nil || providerBucket.Count != 1 || providerBucket.FailedOutcomes != 1 {
		t.Errorf("provider bucket wrong: %+v", providerBucket)
	}
}

// AC #7 — structural test that the legacy provider-reliability metric is
// surfaced as a separate field (not folded into RoutingQuality). Parses the
// service.go AST directly so the assertion is robust to docstring drift.
func TestProviderReliabilityNotRenamedToRoutingQuality(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "service.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}

	candidateFields := findStructFields(file, "RouteCandidateStatus")
	if candidateFields == nil {
		t.Fatalf("RouteCandidateStatus struct not found")
	}
	// Provider-reliability stays as its own scalar, named so the UI can
	// label it distinctly from RoutingQualityMetrics.
	if _, ok := candidateFields["ProviderReliabilityRate"]; !ok {
		t.Fatalf("RouteCandidateStatus.ProviderReliabilityRate missing; available fields: %v", candidateFields)
	}
	// And it is NOT a RoutingQualityMetrics — it must stay scalar.
	if got := candidateFields["ProviderReliabilityRate"]; got != "float64" {
		t.Errorf("ProviderReliabilityRate type = %s, want float64", got)
	}

	reportFields := findStructFields(file, "RouteStatusReport")
	if reportFields == nil {
		t.Fatalf("RouteStatusReport struct not found")
	}
	got, ok := reportFields["RoutingQuality"]
	if !ok {
		t.Fatalf("RouteStatusReport.RoutingQuality missing; available: %v", reportFields)
	}
	if got != "RoutingQualityMetrics" {
		t.Errorf("RouteStatusReport.RoutingQuality type = %s, want RoutingQualityMetrics", got)
	}
	// And RouteStatusReport must NOT have provider-reliability folded onto
	// it — the metric belongs on the per-candidate row, distinct from
	// routing-quality on the parent report.
	for name := range reportFields {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "providerreliability") || strings.Contains(lower, "successrate") {
			t.Errorf("RouteStatusReport unexpectedly carries provider-reliability field %q (should live on RouteCandidateStatus)", name)
		}
	}
}

// AC #1 + AC #2 — structural assertion that both surfaces gained the
// RoutingQuality field with the documented metric set.
func TestRoutingQualityFieldsExposedOnPublicTypes(t *testing.T) {
	report := &RouteStatusReport{}
	if reflect.TypeOf(report.RoutingQuality).Name() != "RoutingQualityMetrics" {
		t.Fatalf("RouteStatusReport.RoutingQuality type = %s, want RoutingQualityMetrics", reflect.TypeOf(report.RoutingQuality).Name())
	}
	usage := &UsageReport{}
	if reflect.TypeOf(usage.RoutingQuality).Name() != "RoutingQualityMetrics" {
		t.Fatalf("UsageReport.RoutingQuality type = %s, want RoutingQualityMetrics", reflect.TypeOf(usage.RoutingQuality).Name())
	}
	rq := RoutingQualityMetrics{}
	rt := reflect.TypeOf(rq)
	for _, name := range []string{"AutoAcceptanceRate", "OverrideDisagreementRate", "OverrideClassBreakdown"} {
		if _, ok := rt.FieldByName(name); !ok {
			t.Errorf("RoutingQualityMetrics missing field %s", name)
		}
	}
	if f, _ := rt.FieldByName("AutoAcceptanceRate"); f.Type.Kind() != reflect.Float64 {
		t.Errorf("AutoAcceptanceRate kind = %s, want float64", f.Type.Kind())
	}
	if f, _ := rt.FieldByName("OverrideDisagreementRate"); f.Type.Kind() != reflect.Float64 {
		t.Errorf("OverrideDisagreementRate kind = %s, want float64", f.Type.Kind())
	}
	if f, _ := rt.FieldByName("OverrideClassBreakdown"); f.Type.Kind() != reflect.Slice {
		t.Errorf("OverrideClassBreakdown kind = %s, want slice", f.Type.Kind())
	}
}

func TestRoutingQualityStoreSnapshotRecent(t *testing.T) {
	st := newRoutingQualityStore()
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		st.recordRequest(base.Add(time.Duration(i)*time.Minute), nil)
	}
	ov := makeOverride([]string{"model"}, nil, 0, false, "", "")
	st.recordRequest(base.Add(5*time.Minute), &ov)
	recs := st.snapshotRecent(0, time.Time{})
	if len(recs) != 6 {
		t.Fatalf("snapshot len = %d, want 6", len(recs))
	}
	m := computeRoutingQualityMetricsFromRecords(recs)
	if m.TotalRequests != 6 || m.TotalOverrides != 1 {
		t.Fatalf("metrics = %+v", m)
	}
	if !floatEq(m.AutoAcceptanceRate, 5.0/6.0) {
		t.Fatalf("AutoAcceptanceRate = %v, want %v", m.AutoAcceptanceRate, 5.0/6.0)
	}
}

// findStructFields parses an *ast.File and returns a map of field name →
// rendered field type for the named struct, or nil if not found.
func findStructFields(file *ast.File, name string) map[string]string {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != name {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			out := make(map[string]string)
			for _, f := range st.Fields.List {
				typeStr := exprString(f.Type)
				for _, n := range f.Names {
					out[n.Name] = typeStr
				}
			}
			return out
		}
	}
	return nil
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.ArrayType:
		return "[]" + exprString(v.Elt)
	case *ast.MapType:
		return "map[" + exprString(v.Key) + "]" + exprString(v.Value)
	default:
		return ""
	}
}
