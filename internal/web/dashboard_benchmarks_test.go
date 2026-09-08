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
		"function caseDetails(a)",
		"function benchmarkRuns(all)",
		"function compareView(name,events)",
		"No behavioral differences",
		"let exact=`${e?.type||\"\"} ${e?.label||\"\"}`.toLowerCase()",
		"/tool|function|command|shell|browser|exec/.test(exact))return\"tools\"",
		"/request.*header|header.*request/.test(exact))return\"model\"",
		"/user|context/.test(exact))return\"input\"",
		"/assistant|model|response|completion|reasoning|llm/.test(exact))return\"model\"",
		"/request|input|message/.test(exact))return\"input\"",
		"Deterministic test case",
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
		"data-stat=\"result\"",
		"Details <span",
		"id=\"benchmark-runs\"",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("dashboard markup missing %q", want)
		}
	}
	for _, removed := range []string{"scenario-results", "Benchmark scenario results"} {
		if strings.Contains(string(html), removed) {
			t.Fatalf("obsolete scenario panel still rendered: %q", removed)
		}
	}
}
