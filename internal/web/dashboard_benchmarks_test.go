package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardRendersAndSubmitsBenchmarkTasks(t *testing.T) {
	js, err := os.ReadFile(filepath.Join("static", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"benchmarks:[]",
		"benchmarkTasks=()=>$$('#benchmark-list input:checked')",
		"function benchmarkSuite(suite,selected)",
		"task.token_budget",
		"benchmark_task_ids:benchmarkTasks()",
		"local=cases().length,benchmark=benchmarkTasks().length",
		"function scenarioResults(all)",
		"Deterministic verifier:",
		"[\"attempt\",\"progress\"]",
	} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("dashboard benchmark integration missing %q", want)
		}
	}

	html, err := os.ReadFile(filepath.Join("static", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<legend>3. Test cases</legend>",
		"<legend>4. Benchmarks</legend>",
		"id=\"benchmark-list\"",
		"aria-live=\"polite\"",
		"id=\"scenario-results\"",
		"Each harness verdict appears as soon as that agent finishes.",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("dashboard markup missing %q", want)
		}
	}
}
