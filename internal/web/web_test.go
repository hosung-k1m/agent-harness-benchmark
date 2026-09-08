package web

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hosungkim/agent-harness-benchmark/internal/manifest"
	"github.com/hosungkim/agent-harness-benchmark/internal/model"
	"github.com/hosungkim/agent-harness-benchmark/internal/run"
)

func fakeVariant(id string) run.Variant {
	return run.Variant{Variant: manifest.Variant{ID: id, Image: "test", ImageDigest: "sha256:test", Adapter: "/opt/bench/test", Model: manifest.Model, ReasoningEffort: manifest.Reasoning, NetworkPolicyID: manifest.NetworkPolicy}}
}

func TestBatchConfigurationIsValidatedAndSnapshotted(t *testing.T) {
	got := make(chan run.Variant, 1)
	s, h, _ := newTestServer(t, func(_ context.Context, v run.Variant, _ run.Request) run.Outcome {
		got <- v
		return run.Outcome{Dir: "artifact", Result: model.Result{Status: model.StatusCompleted, Usage: model.Usage{TokenQuality: model.TokenUnavailable}}}
	})
	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"harness_ids":["codex-cli"],"case_ids":["one"],"model":"nope","reasoning_effort":"high"}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid config: %d %s", bad.Code, bad.Body.String())
	}
	good := httptest.NewRecorder()
	h.ServeHTTP(good, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"harness_ids":["dsh-default-codex"],"case_ids":["one"],"model":"gpt-5.6-terra","reasoning_effort":"medium"}`)))
	if good.Code != http.StatusCreated {
		t.Fatalf("valid config: %d %s", good.Code, good.Body.String())
	}
	var batch Batch
	_ = json.NewDecoder(good.Body).Decode(&batch)
	if batch.Attempts[0].Model != "gpt-5.6-terra" || batch.Attempts[0].ReasoningEffort != "medium" || !batch.Attempts[0].TrajectoryAvailable || batch.Attempts[0].HarnessKind != "deepseek-harness" {
		t.Fatalf("snapshot = %#v", batch.Attempts[0])
	}
	select {
	case v := <-got:
		if v.Model != "gpt-5.6-terra" || v.ReasoningEffort != "medium" {
			t.Fatalf("execution variant = %#v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("executor not called")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		done := s.batches[batch.ID].Status == "completed"
		s.mu.RUnlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	reloaded, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.batches[batch.ID].Attempts[0]
	if persisted.Model != "gpt-5.6-terra" || persisted.ReasoningEffort != "medium" || persisted.DisplayName == "" || !persisted.TrajectoryAvailable {
		t.Fatalf("persisted snapshot = %#v", persisted)
	}
}

func TestCatalogExposesThreeHarnessChoicesAndMatrix(t *testing.T) {
	root := t.TempDir()
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("variants", 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"codex-cli", "dsh-default-codex", "dsh-modified-codex"} {
		if err := os.WriteFile(filepath.Join("variants", id+".json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, h, _ := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome { return run.Outcome{} })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/catalog", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Harnesses []Harness `json:"harnesses"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Harnesses) != 3 {
		t.Fatalf("harnesses = %#v", out.Harnesses)
	}
	for _, item := range out.Harnesses {
		if len(item.SupportedModels) == 0 {
			t.Fatalf("missing matrix: %#v", item)
		}
	}
}

func TestTrajectoryAndComparisonSafety(t *testing.T) {
	s, h, _ := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome { return run.Outcome{} })
	makeAttempt := func(id, caseID string, event string) *Attempt {
		dir := filepath.Join(t.TempDir(), "attempt")
		if err := os.MkdirAll(filepath.Join(dir, "out"), 0700); err != nil {
			t.Fatal(err)
		}
		data := `{"schema_version":"1","source":"dsh","duration_ms":12,"events":[` + event + `]}`
		if err := os.WriteFile(filepath.Join(dir, "out", "trajectory.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return &Attempt{ID: id, BatchID: "b", CaseID: caseID, VariantID: "dsh-default-codex", HarnessKind: "deepseek-harness", TrajectoryAvailable: true, Status: "completed", Artifact: dir, Result: &model.Result{Status: model.StatusCompleted}}
	}
	left, right := makeAttempt("aaaa", "one", `{"type":"tool"}`), makeAttempt("bbbb", "one", `{"type":"answer"}`)
	s.mu.Lock()
	s.batches["b"] = &Batch{ID: "b", Attempts: []*Attempt{left, right}}
	s.mu.Unlock()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/attempts/aaaa/trajectory", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"event_count":1`) {
		t.Fatalf("trajectory: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/trajectories/compare", strings.NewReader(`{"attempt_ids":["aaaa","bbbb"]}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"same":false`) {
		t.Fatalf("comparison: %d %s", w.Code, w.Body.String())
	}
	// A symlinked trajectory that escapes the owned out directory must never be exposed.
	escape := filepath.Join(t.TempDir(), "secret.json")
	_ = os.WriteFile(escape, []byte(`{"schema_version":"1","source":"dsh","events":[]}`), 0600)
	_ = os.Remove(filepath.Join(left.Artifact, "out", "trajectory.json"))
	if err := os.Symlink(escape, filepath.Join(left.Artifact, "out", "trajectory.json")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/attempts/aaaa/trajectory", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("escaped file: %d %s", w.Code, w.Body.String())
	}
}

func TestTrajectoryComparisonIgnoresVolatileEventMetadata(t *testing.T) {
	left := json.RawMessage(`{"sequence":1,"relative_ms":10,"timestamp":"2026-01-01T00:00:00Z","type":"tool","category":"tool","label":"search","detail":"docs","usage_delta":{"input_tokens":3}}`)
	timeShifted := json.RawMessage(`{"index":44,"sequence":9,"relative_ms":999,"time":"later","type":"tool","category":"tool","label":"search","detail":"docs","usage_delta":{"input_tokens":3}}`)
	if !semanticEventEqual(left, timeShifted) {
		t.Fatal("time and sequence-only changes must compare as the same event")
	}
	semanticChange := json.RawMessage(`{"sequence":2,"relative_ms":11,"type":"tool","category":"tool","label":"search","detail":"different docs","usage_delta":{"input_tokens":3}}`)
	if semanticEventEqual(left, semanticChange) {
		t.Fatal("semantic detail changes must compare as changed")
	}
}

func TestAttemptVerdictIsPublishedBeforeSiblingAgentFinishes(t *testing.T) {
	started := make(chan string, 2)
	releases := map[string]chan struct{}{"one": make(chan struct{}), "two": make(chan struct{})}
	s, h, _ := newTestServer(t, func(_ context.Context, _ run.Variant, req run.Request) run.Outcome {
		started <- req.CaseID
		<-releases[req.CaseID]
		passed := true
		return run.Outcome{Dir: "artifact-" + req.CaseID, Result: model.Result{Status: model.StatusCompleted, VerifierPassed: &passed, Usage: model.Usage{TokenQuality: model.TokenUnavailable}}}
	})

	request := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"harness_ids":["codex-cli"],"case_ids":["one","two"],"workers":2}`))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create batch: %d %s", response.Code, response.Body.String())
	}
	var batch Batch
	if err := json.NewDecoder(response.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("agents did not start")
		}
	}

	events := make(chan Event, 8)
	s.mu.Lock()
	s.subs[events] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, events)
		s.mu.Unlock()
	}()

	close(releases["one"])
	var completed *Attempt
	deadline := time.After(time.Second)
	for completed == nil {
		select {
		case event := <-events:
			if event.Type == "attempt" && event.Attempt != nil && event.Attempt.CaseID == "one" && event.Attempt.Result != nil {
				completed = event.Attempt
			}
		case <-deadline:
			t.Fatal("completed attempt was not published")
		}
	}
	if completed.Verdict != "pass" || completed.Result.VerifierPassed == nil || !*completed.Result.VerifierPassed {
		t.Fatalf("published result = %#v", completed)
	}
	s.mu.RLock()
	batchStatus := s.batches[batch.ID].Status
	secondStatus := s.batches[batch.ID].Attempts[1].Status
	s.mu.RUnlock()
	if batchStatus != "running" || secondStatus != "running" {
		t.Fatalf("completion waited for sibling: batch=%s sibling=%s", batchStatus, secondStatus)
	}

	close(releases["two"])
	for limit := time.Now().Add(time.Second); time.Now().Before(limit); {
		s.mu.RLock()
		done := s.batches[batch.ID].Status == "completed"
		s.mu.RUnlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
}

func newTestServer(t *testing.T, execute func(context.Context, run.Variant, run.Request) run.Outcome) (*Server, http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	cases := filepath.Join(root, "cases")
	for _, id := range []string{"one", "two"} {
		if err := os.MkdirAll(filepath.Join(cases, id, "fixture"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cases, id, "prompt.txt"), []byte("do work"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{CasesDir: cases, DataDir: filepath.Join(root, "history"), Workers: 2, LoadVariant: func(id string) (run.Variant, error) { return fakeVariant(id), nil }, Execute: execute})
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Handler(""), cases
}
func TestBatchCreatesIndependentCrossProductAttempts(t *testing.T) {
	var mu sync.Mutex
	got := map[string]bool{}
	_, h, _ := newTestServer(t, func(_ context.Context, v run.Variant, r run.Request) run.Outcome {
		mu.Lock()
		got[v.ID+":"+r.CaseID] = true
		mu.Unlock()
		return run.Outcome{Dir: "artifact", Result: model.Result{Status: model.StatusCompleted, ElapsedMillis: 7, Usage: model.Usage{TokenQuality: model.TokenUnavailable}}}
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewBufferString(`{"harness_ids":["codex-cli","dsh-default-codex"],"case_ids":["one","two"],"workers":2}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var b Batch
	if err := json.NewDecoder(rr.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	if len(b.Attempts) != 4 {
		t.Fatalf("attempt count %d", len(b.Attempts))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q := httptest.NewRecorder()
		h.ServeHTTP(q, httptest.NewRequest("GET", "/api/runs/"+b.ID, nil))
		var current Batch
		_ = json.NewDecoder(q.Body).Decode(&current)
		if current.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("not independent cross product: %#v", got)
	}
}

func TestBenchmarkCatalogAndBatchSelection(t *testing.T) {
	root := t.TempDir()
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("variants", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("variants", "codex-cli.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var received []run.Request
	var calls int
	s, h, cases := newTestServer(t, func(_ context.Context, _ run.Variant, request run.Request) run.Outcome {
		mu.Lock()
		received = append(received, request)
		calls++
		mu.Unlock()
		return run.Outcome{Dir: "artifact", Result: model.Result{Status: model.StatusCompleted, Usage: model.Usage{TokenQuality: model.TokenUnavailable}}}
	})
	benchmarks := filepath.Join(filepath.Dir(cases), "benchmarks")
	task := filepath.Join(benchmarks, "bfcl", "tasks", "get-weather")
	if err := os.MkdirAll(filepath.Join(task, "fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(task, "hidden"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task, "prompt.txt"), []byte("call get_weather"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"id":"bfcl","name":"BFCL","task_count":99,"upstream":{"name":"Berkeley","revision":"abc"},"evaluator":{"type":"trusted-hidden-verifier"},"tasks":[{"id":"get-weather","title":"Get weather","path":"tasks/get-weather","token_budget":"micro","provenance":{"source_id":"weather-1"}}]}`
	if err := os.MkdirAll(filepath.Join(benchmarks, "bfcl"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(benchmarks, "bfcl", "benchmark.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.BenchmarksDir = benchmarks
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/catalog", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"task_count":1`) || !strings.Contains(w.Body.String(), `"task_id":"get-weather"`) || !strings.Contains(w.Body.String(), `"token_budget":"micro"`) || !strings.Contains(w.Body.String(), `"provenance":{"source_id":"weather-1"}`) {
		t.Fatalf("benchmark catalog: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"harness_ids":["codex-cli"],"case_ids":["one"],"benchmark_task_ids":["bfcl/get-weather"]}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create benchmark batch: %d %s", w.Code, w.Body.String())
	}
	var batch Batch
	if err := json.NewDecoder(w.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Attempts) != 2 {
		t.Fatalf("attempts = %#v", batch.Attempts)
	}
	var benchmarkAttempt *Attempt
	for _, attempt := range batch.Attempts {
		if attempt.BenchmarkID == "bfcl" {
			benchmarkAttempt = attempt
		}
	}
	if benchmarkAttempt == nil || benchmarkAttempt.BenchmarkTaskID != "get-weather" || benchmarkAttempt.CaseID != "bfcl/get-weather" {
		t.Fatalf("benchmark attempt = %#v", benchmarkAttempt)
	}
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		count := calls
		mu.Unlock()
		if count >= 2 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	var benchmarkRequest *run.Request
	for i := range received {
		if received[i].BenchmarkID == "bfcl" {
			benchmarkRequest = &received[i]
		}
	}
	wantHidden, err := filepath.EvalSymlinks(filepath.Join(task, "hidden"))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || benchmarkRequest == nil || benchmarkRequest.BenchmarkTaskID != "get-weather" || benchmarkRequest.Hidden != wantHidden || !benchmarkRequest.Coding {
		t.Fatalf("runner requests = %#v, calls=%d", received, calls)
	}
	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"harness_ids":["codex-cli"],"benchmark_task_ids":["bfcl/nope"]}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown benchmark task: %d %s", bad.Code, bad.Body.String())
	}
}

func TestBenchmarkTaskPathRejectsSymlinkEscape(t *testing.T) {
	s, _, _ := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome { return run.Outcome{} })
	root := t.TempDir()
	s.cfg.BenchmarksDir = root
	if err := os.MkdirAll(filepath.Join(root, "bfcl", "tasks"), 0700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "bfcl", "tasks", "escaped")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.safeBenchmarkTaskDir("bfcl", "escaped"); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}
func TestImportRejectsTraversalAndLeavesNoCase(t *testing.T) {
	_, h, cases := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome { return run.Outcome{} })
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", "safe-case")
	_ = mw.WriteField("prompt", "hello")
	_ = mw.WriteField("file_paths", `["../escape.txt"]`)
	part, err := mw.CreateFormFile("files", "escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("bad"))
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/cases", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cases, "safe-case")); !os.IsNotExist(err) {
		t.Fatalf("unsafe case remained: %v", err)
	}
}
func TestImportRejectsTarTraversal(t *testing.T) {
	_, h, cases := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome { return run.Outcome{} })
	var tarbuf bytes.Buffer
	tw := tar.NewWriter(&tarbuf)
	_ = tw.WriteHeader(&tar.Header{Name: "../outside", Size: 1, Mode: 0600})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("id", "bad-tar")
	_ = mw.WriteField("prompt", "hello")
	p, _ := mw.CreateFormFile("archive", "bad.tar")
	_, _ = io.Copy(p, &tarbuf)
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/api/cases", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cases, "bad-tar")); !os.IsNotExist(err) {
		t.Fatal("unsafe archive was persisted")
	}
}

func TestImportRequiresSupportedNonEmptySource(t *testing.T) {
	_, h, cases := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome { return run.Outcome{} })
	for i, source := range []string{"", "files", "unknown"} {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("id", []string{"empty-source", "empty-files", "empty-unknown"}[i])
		_ = mw.WriteField("prompt", "hello")
		_ = mw.WriteField("source_type", source)
		_ = mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/api/cases", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("source %q: got %d %s", source, w.Code, w.Body.String())
		}
	}
	entries, err := os.ReadDir(cases)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("invalid imports left staged cases: %#v", entries)
	}
}

func TestMutationsRejectCrossOriginBrowserRequests(t *testing.T) {
	_, h, _ := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome { return run.Outcome{} })
	r := httptest.NewRequest(http.MethodPost, "http://bench.local/api/runs", bytes.NewBufferString(`{"harness_ids":["codex-cli"],"case_ids":["one"]}`))
	r.Header.Set("Origin", "https://malicious.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request: got %d %s", w.Code, w.Body.String())
	}
}
