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
