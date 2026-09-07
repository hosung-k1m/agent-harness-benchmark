package web

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hosungkim/agent-harness-benchmark/internal/model"
	"github.com/hosungkim/agent-harness-benchmark/internal/run"
)

// TestDashboardE2E verifies the externally visible run lifecycle: requests are
// expanded into independent case/harness executions, worker limits are obeyed,
// and a reload can recover the completed batch from disk.
func TestDashboardE2EConcurrencyAndPersistentHistory(t *testing.T) {
	var current, maximum atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	root := t.TempDir()
	cases := filepath.Join(root, "cases")
	for _, id := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(cases, id, "fixture"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cases, id, "prompt.txt"), []byte(id+" prompt"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		CasesDir: cases, DataDir: filepath.Join(root, "history"), Workers: 4,
		LoadVariant: func(id string) (run.Variant, error) { return fakeVariant(id), nil },
		Execute: func(_ context.Context, v run.Variant, r run.Request) run.Outcome {
			n := current.Add(1)
			for {
				old := maximum.Load()
				if n <= old || maximum.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			<-release
			current.Add(-1)
			return run.Outcome{Dir: "artifact-" + v.ID + "-" + r.CaseID, Result: model.Result{Status: model.StatusCompleted, ElapsedMillis: 11, Usage: model.Usage{TokenQuality: model.TokenUnavailable}}}
		},
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler("")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"harness_ids":["codex-cli","dsh-default-codex"],"case_ids":["alpha","beta"],"workers":1}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create batch: %d %s", rr.Code, rr.Body.String())
	}
	var created Batch
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if got := len(created.Attempts); got != 4 {
		t.Fatalf("cross product attempts = %d, want 4", got)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("attempt did not start")
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum execution concurrency = %d, want 1", got)
	}
	// The executor cannot begin a second job while the one worker remains blocked.
	select {
	case <-started:
		t.Fatal("worker limit was exceeded")
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/api/runs/"+created.ID, nil))
		var b Batch
		_ = json.NewDecoder(out.Body).Decode(&b)
		if b.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	reloaded, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := httptest.NewRecorder()
	reloaded.Handler("").ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/api/runs/"+created.ID, nil))
	if got.Code != http.StatusOK {
		t.Fatalf("persisted batch lookup: %d %s", got.Code, got.Body.String())
	}
	var stored Batch
	if err := json.NewDecoder(got.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status != "completed" || len(stored.Attempts) != 4 {
		t.Fatalf("bad persisted batch: %#v", stored)
	}
	for _, a := range stored.Attempts {
		if a.Status != "completed" || a.Result == nil {
			t.Fatalf("unfinished persisted attempt: %#v", a)
		}
	}
}

func TestDashboardE2ESSEProgressThenProviderUsage(t *testing.T) {
	s, h, _ := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome {
		time.Sleep(1100 * time.Millisecond)
		return run.Outcome{Dir: "artifact", Result: model.Result{Status: model.StatusCompleted, ElapsedMillis: 1111, Usage: model.Usage{InputTokens: model.Int64(7), OutputTokens: model.Int64(9), TokenQuality: model.TokenProviderReported}}}
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	streamReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	type streamResult struct {
		response *http.Response
		err      error
	}
	streamResultCh := make(chan streamResult, 1)
	go func() {
		response, err := http.DefaultClient.Do(streamReq)
		streamResultCh <- streamResult{response, err}
	}()
	// The handler registers subscribers before emitting response bytes. Waiting for
	// it removes a timing race between opening SSE and starting the benchmark.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		subscribed := len(s.subs) == 1
		s.mu.RUnlock()
		if subscribed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	post, err := http.Post(ts.URL+"/api/runs", "application/json", strings.NewReader(`{"harness_ids":["codex-cli"],"case_ids":["one"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer post.Body.Close()
	if post.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(post.Body)
		t.Fatalf("create: %s %s", post.Status, body)
	}
	streamOutcome := <-streamResultCh
	if streamOutcome.err != nil {
		t.Fatal(streamOutcome.err)
	}
	stream := streamOutcome.response
	defer stream.Body.Close()
	if ct := stream.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE content type %q", ct)
	}

	scanner := bufio.NewScanner(stream.Body)
	var eventType string
	seenProgress, seenFinal := false, false
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Attempt == nil {
			continue
		}
		if eventType == "progress" {
			seenProgress = event.Attempt.Status == "running" && event.Attempt.ElapsedMillis >= 1000 && event.Attempt.Result == nil
		}
		if eventType == "attempt" && event.Attempt.Status == "completed" {
			u := event.Attempt.Result.Usage
			seenFinal = u.TokenQuality == model.TokenProviderReported && u.InputTokens != nil && *u.InputTokens == 7 && u.OutputTokens != nil && *u.OutputTokens == 9 && event.Attempt.ElapsedMillis == 1111
		}
		if seenProgress && seenFinal {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !seenProgress || !seenFinal {
		t.Fatalf("SSE missing progress/final provider-usage transition: progress=%v final=%v", seenProgress, seenFinal)
	}
}

func TestDashboardE2EImportsFilesAndTGZAndRejectsInvalidHTTPSRepo(t *testing.T) {
	_, h, cases := newTestServer(t, func(context.Context, run.Variant, run.Request) run.Outcome { return run.Outcome{} })
	upload := func(id, source, repository string, files map[string]string, archive []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("id", id)
		_ = mw.WriteField("prompt", "solve it")
		_ = mw.WriteField("source_type", source)
		if repository != "" {
			_ = mw.WriteField("repository", repository)
		}
		if len(files) > 0 {
			paths := make([]string, 0, len(files))
			for path, contents := range files {
				paths = append(paths, path)
				p, _ := mw.CreateFormFile("files", filepath.Base(path))
				_, _ = io.WriteString(p, contents)
			}
			encoded, _ := json.Marshal(paths)
			_ = mw.WriteField("file_paths", string(encoded))
		}
		if archive != nil {
			p, _ := mw.CreateFormFile("archive", "fixture.tgz")
			_, _ = p.Write(archive)
		}
		_ = mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/api/cases", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := upload("files-case", "files", "", map[string]string{"src/main.txt": "hello"}, nil); w.Code != http.StatusCreated {
		t.Fatalf("file import: %d %s", w.Code, w.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(cases, "files-case", "fixture", "src", "main.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("file fixture: %q %v", b, err)
	}

	var tarBytes bytes.Buffer
	gw := gzip.NewWriter(&tarBytes)
	// A minimal tar is enough to exercise gzip/tar archive routing without relying on an external repository.
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0600, Size: 2})
	_, _ = tw.Write([]byte("ok"))
	_ = tw.Close()
	_ = gw.Close()
	if w := upload("tgz-case", "archive", "", nil, tarBytes.Bytes()); w.Code != http.StatusCreated {
		t.Fatalf("tgz import: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cases, "tgz-case", "fixture", "README.md")); err != nil {
		t.Fatalf("tgz fixture: %v", err)
	}
	// Archive wrappers are flattened only when the sole directory matches the
	// archive basename; this preserves a genuine top-level src/ directory while
	// making standard repository downloads convenient to run.
	var wrappedTar bytes.Buffer
	wrappedGzip := gzip.NewWriter(&wrappedTar)
	wrappedWriter := tar.NewWriter(wrappedGzip)
	_ = wrappedWriter.WriteHeader(&tar.Header{Name: "fixture/README.md", Mode: 0600, Size: 7})
	_, _ = wrappedWriter.Write([]byte("wrapped"))
	_ = wrappedWriter.Close()
	_ = wrappedGzip.Close()
	if w := upload("wrapped-case", "archive", "", nil, wrappedTar.Bytes()); w.Code != http.StatusCreated {
		t.Fatalf("wrapped archive import: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cases, "wrapped-case", "fixture", "README.md")); err != nil {
		t.Fatalf("archive wrapper was not flattened: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cases, "wrapped-case", "fixture", "fixture")); !os.IsNotExist(err) {
		t.Fatalf("archive wrapper remained after normalization: %v", err)
	}
	var zipBytes bytes.Buffer
	zw := zip.NewWriter(&zipBytes)
	zf, err := zw.Create("src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zf.Write([]byte("package main"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if w := upload("zip-case", "archive", "", nil, zipBytes.Bytes()); w.Code != http.StatusCreated {
		t.Fatalf("zip import: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cases, "zip-case", "fixture", "src", "main.go")); err != nil {
		t.Fatalf("zip fixture: %v", err)
	}

	// This malformed/offline URL is rejected before git is invoked, so the test has no network dependency.
	if w := upload("invalid-repo", "repo_url", "https://", nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid HTTPS repo: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cases, "invalid-repo")); !os.IsNotExist(err) {
		t.Fatalf("invalid repository left a case behind: %v", err)
	}
}

// The server intentionally nests final provider output under result.  The UI
// also maintains a (possibly empty) top-level usage object while merging SSE
// events, so result usage must win or final token totals disappear.
func TestDashboardStaticPrefersFinalResultUsage(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("static", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		"use=a=>a.result?.usage||a.usage||{}",
		"let x=$(\"#attempt-template\").content.firstElementChild.cloneNode(true),u=use(a),r=a.result||{}",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("dashboard must prefer final result usage; missing %q", want)
		}
	}
}
