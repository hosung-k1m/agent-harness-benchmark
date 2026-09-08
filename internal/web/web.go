// Package web exposes the benchmark runner through a small JSON/SSE HTTP API.
package web

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hosungkim/agent-harness-benchmark/internal/artifact"
	"github.com/hosungkim/agent-harness-benchmark/internal/manifest"
	"github.com/hosungkim/agent-harness-benchmark/internal/model"
	"github.com/hosungkim/agent-harness-benchmark/internal/run"
)

const (
	maxUploadBytes   = 128 << 20
	maxBatchAttempts = 128
)

type Case struct {
	ID         string `json:"id"`
	Prompt     string `json:"prompt"`
	HasFixture bool   `json:"has_fixture"`
}

// Benchmark describes an on-disk benchmark suite. Tasks live directly below
// the suite's benchmark.json file and use the same prompt/fixture/hidden
// layout as local cases.
type Benchmark struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Provenance  json.RawMessage `json:"provenance,omitempty"`
	Evaluator   json.RawMessage `json:"evaluator,omitempty"`
	TaskCount   int             `json:"task_count"`
	Tasks       []BenchmarkTask `json:"tasks"`
}
type BenchmarkTask struct {
	ID          string          `json:"id"`
	BenchmarkID string          `json:"benchmark_id"`
	TaskID      string          `json:"task_id"`
	Name        string          `json:"name"`
	Prompt      string          `json:"prompt"`
	Provenance  json.RawMessage `json:"provenance,omitempty"`
	Evaluator   json.RawMessage `json:"evaluator,omitempty"`
	TokenBudget json.RawMessage `json:"token_budget,omitempty"`
	HasFixture  bool            `json:"has_fixture"`
}
type Harness struct {
	ID                  string        `json:"id"`
	DisplayName         string        `json:"display_name"`
	Kind                string        `json:"kind"`
	TrajectoryAvailable bool          `json:"trajectory_available"`
	SupportedModels     []ModelOption `json:"supported_models"`
	Model               string        `json:"model"`
	ReasoningEffort     string        `json:"reasoning_effort"`
	PluginMode          string        `json:"plugin_mode,omitempty"`
	PluginRevision      string        `json:"plugin_revision,omitempty"`
}
type ModelOption struct {
	ID               string   `json:"id"`
	ReasoningEfforts []string `json:"reasoning_efforts"`
}
type Attempt struct {
	ID                  string          `json:"id"`
	BatchID             string          `json:"batch_id"`
	CreatedAt           time.Time       `json:"created_at"`
	CaseID              string          `json:"case_id"`
	BenchmarkID         string          `json:"benchmark_id,omitempty"`
	BenchmarkTaskID     string          `json:"benchmark_task_id,omitempty"`
	Provenance          json.RawMessage `json:"provenance,omitempty"`
	Evaluator           json.RawMessage `json:"evaluator,omitempty"`
	VariantID           string          `json:"variant_id"`
	DisplayName         string          `json:"display_name,omitempty"`
	HarnessKind         string          `json:"harness_kind,omitempty"`
	TrajectoryAvailable bool            `json:"trajectory_available"`
	Model               string          `json:"model,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	PluginMode          string          `json:"plugin_mode,omitempty"`
	PluginRevision      string          `json:"plugin_revision,omitempty"`
	Status              string          `json:"status"`
	StartedAt           *time.Time      `json:"started_at,omitempty"`
	FinishedAt          *time.Time      `json:"finished_at,omitempty"`
	ElapsedMillis       int64           `json:"elapsed_millis"`
	Result              *model.Result   `json:"result,omitempty"`
	Verdict             string          `json:"verdict,omitempty"`
	Artifact            string          `json:"artifact,omitempty"`
	Error               string          `json:"error,omitempty"`
}
type Batch struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	Status    string     `json:"status"`
	Attempts  []*Attempt `json:"attempts"`
}
type Event struct {
	Type    string    `json:"type"`
	BatchID string    `json:"batch_id"`
	Attempt *Attempt  `json:"attempt,omitempty"`
	At      time.Time `json:"at"`
}

// Config makes execution replaceable for tests and lets the CLI own Docker/image validation.
type Config struct {
	CasesDir, BenchmarksDir, DataDir, ArtifactRoot string
	Workers                                        int
	LoadVariant                                    func(string) (run.Variant, error)
	Execute                                        func(context.Context, run.Variant, run.Request) run.Outcome
}
type Server struct {
	cfg     Config
	mu      sync.RWMutex
	batches map[string]*Batch
	subs    map[chan Event]struct{}
}

func New(cfg Config) (*Server, error) {
	if cfg.CasesDir == "" {
		cfg.CasesDir = "cases"
	}
	if cfg.BenchmarksDir == "" {
		cfg.BenchmarksDir = "benchmarks"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = ".bench/batches"
	}
	if cfg.ArtifactRoot == "" {
		cfg.ArtifactRoot = filepath.Join(filepath.Dir(cfg.DataDir), "runs")
	}
	if cfg.Workers < 1 {
		cfg.Workers = 2
	}
	if cfg.LoadVariant == nil || cfg.Execute == nil {
		return nil, errors.New("web server requires variant loader and executor")
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, batches: map[string]*Batch{}, subs: map[chan Event]struct{}{}}
	_ = s.loadHistory() // Corrupt historical files must not prevent access to valid records.
	return s, nil
}

func (s *Server) Handler(staticDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/catalog", s.catalog)
	mux.HandleFunc("GET /api/harnesses", s.harnesses)
	mux.HandleFunc("GET /api/cases", s.cases)
	mux.HandleFunc("POST /api/cases", s.importCase)
	mux.HandleFunc("GET /api/runs", s.listBatches)
	mux.HandleFunc("POST /api/runs", s.createBatch)
	mux.HandleFunc("DELETE /api/runs", s.deleteBatches)
	mux.HandleFunc("GET /api/runs/", s.getBatch)
	mux.HandleFunc("DELETE /api/runs/", s.deleteBatch)
	mux.HandleFunc("GET /api/batches", s.listBatches)
	mux.HandleFunc("POST /api/batches", s.createBatch)
	mux.HandleFunc("DELETE /api/batches", s.deleteBatches)
	mux.HandleFunc("GET /api/batches/", s.getBatch)
	mux.HandleFunc("DELETE /api/batches/", s.deleteBatch)
	mux.HandleFunc("GET /api/attempts/", s.getAttemptArtifact)
	mux.HandleFunc("GET /api/trajectories/compare", s.compareTrajectories)
	mux.HandleFunc("POST /api/trajectories/compare", s.compareTrajectories)
	// Kept as a readable dashboard alias for the comparison view.
	mux.HandleFunc("GET /api/trajectory-comparison", s.compareTrajectories)
	mux.HandleFunc("GET /api/events", s.events)
	if staticDir != "" {
		if info, err := os.Stat(staticDir); err == nil && info.IsDir() {
			mux.Handle("/", http.FileServer(http.Dir(staticDir)))
		}
	}
	return securityHeaders(sameOrigin(mux))
}

func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					apiError(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, status int, err error) {
	jsonOut(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) harnesses(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir("variants")
	if err != nil {
		apiError(w, 500, err)
		return
	}
	out := make([]Harness, 0, len(entries))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		v, err := s.cfg.LoadVariant(id)
		if err != nil {
			continue
		}
		out = append(out, describeVariant(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	jsonOut(w, 200, out)
}
func (s *Server) catalog(w http.ResponseWriter, _ *http.Request) {
	// The detailed endpoints remain useful to API consumers; this is the dashboard's one-call bootstrap view.
	entries, err := os.ReadDir("variants")
	if err != nil {
		apiError(w, 500, err)
		return
	}
	h := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		v, e := s.cfg.LoadVariant(id)
		if e != nil {
			h = append(h, map[string]any{"id": id, "name": id, "available": false})
			continue
		}
		d := describeVariant(v)
		h = append(h, map[string]any{"id": d.ID, "name": d.DisplayName, "description": d.Model + " / " + d.ReasoningEffort, "available": true,
			"kind": d.Kind, "trajectory_available": d.TrajectoryAvailable, "supported_models": d.SupportedModels,
			"model": d.Model, "reasoning_effort": d.ReasoningEffort, "plugin_mode": d.PluginMode, "plugin_revision": d.PluginRevision})
	}
	c, err := s.caseList()
	if err != nil {
		apiError(w, 500, err)
		return
	}
	cs := make([]map[string]any, 0, len(c))
	for _, x := range c {
		preview := x.Prompt
		if len(preview) > 180 {
			preview = preview[:180]
		}
		cs = append(cs, map[string]any{"id": x.ID, "name": x.ID, "prompt_preview": preview, "source_type": "files"})
	}
	benchmarks, err := s.benchmarkList()
	if err != nil {
		apiError(w, 500, err)
		return
	}
	jsonOut(w, 200, map[string]any{"harnesses": h, "cases": cs, "benchmarks": benchmarks})
}

func describeVariant(v run.Variant) Harness {
	h := Harness{ID: v.ID, DisplayName: v.DisplayName, Model: v.Model, ReasoningEffort: v.ReasoningEffort}
	if h.DisplayName == "" {
		h.DisplayName = v.ID
	}
	for _, id := range sortedModelIDs() {
		h.SupportedModels = append(h.SupportedModels, ModelOption{ID: id, ReasoningEfforts: append([]string(nil), manifest.SupportedModels[id]...)})
	}
	switch v.ID {
	case "dsh-default-codex":
		h.Kind, h.TrajectoryAvailable, h.PluginMode = "deepseek-harness", true, "codex"
		h.PluginRevision = "default"
	case "dsh-modified-codex":
		h.Kind, h.TrajectoryAvailable, h.PluginMode = "deepseek-harness", true, "codex"
		h.PluginRevision = "modified"
	default:
		h.Kind = "codex-cli"
	}
	return h
}

func sortedModelIDs() []string {
	ids := make([]string, 0, len(manifest.SupportedModels))
	for id := range manifest.SupportedModels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func safeID(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (s *Server) caseList() ([]Case, error) {
	entries, err := os.ReadDir(s.cfg.CasesDir)
	if os.IsNotExist(err) {
		return []Case{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Case{}
	for _, e := range entries {
		if !e.IsDir() || !safeID(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.cfg.CasesDir, e.Name(), "prompt.txt"))
		if err != nil {
			continue
		}
		_, err = os.Stat(filepath.Join(s.cfg.CasesDir, e.Name(), "fixture"))
		out = append(out, Case{e.Name(), string(b), err == nil})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// benchmarkList deliberately discovers task directories instead of trusting a
// manifest task list. This keeps suites portable: a task is runnable exactly
// when its prompt and fixture are present on disk. Manifest task data is used
// only to enrich names and evaluator/provenance metadata.
func (s *Server) benchmarkList() ([]Benchmark, error) {
	entries, err := os.ReadDir(s.cfg.BenchmarksDir)
	if os.IsNotExist(err) {
		return []Benchmark{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Benchmark{}
	for _, entry := range entries {
		if !entry.IsDir() || !safeID(entry.Name()) {
			continue
		}
		dir := filepath.Join(s.cfg.BenchmarksDir, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, "benchmark.json"))
		if err != nil {
			continue
		}
		var manifest struct {
			ID, Name, Description           string
			TaskCount                       int `json:"task_count"`
			Provenance, Evaluator, Upstream json.RawMessage
			Tasks                           []struct {
				ID                    string
				Name                  string
				Title                 string
				Path                  string
				Provenance, Evaluator json.RawMessage
				TokenBudget           json.RawMessage `json:"token_budget"`
			} `json:"tasks"`
		}
		if json.Unmarshal(data, &manifest) != nil {
			continue
		}
		// Directory name is the authority for safe, stable references. A suite
		// may omit id, but cannot point this server at another directory.
		if manifest.ID != "" && manifest.ID != entry.Name() {
			continue
		}
		b := Benchmark{ID: entry.Name(), Name: manifest.Name, Description: manifest.Description, Provenance: manifest.Provenance, Evaluator: manifest.Evaluator, TaskCount: manifest.TaskCount}
		if b.Name == "" {
			b.Name = b.ID
		}
		if len(b.Provenance) == 0 {
			b.Provenance = manifest.Upstream
		}
		meta := map[string]struct {
			Name                               string
			Provenance, Evaluator, TokenBudget json.RawMessage
		}{}
		for _, task := range manifest.Tasks {
			name := task.Name
			if name == "" {
				name = task.Title
			}
			meta[task.ID] = struct {
				Name                               string
				Provenance, Evaluator, TokenBudget json.RawMessage
			}{name, task.Provenance, task.Evaluator, task.TokenBudget}
		}
		taskRoot := filepath.Join(dir, "tasks")
		taskEntries, err := os.ReadDir(taskRoot)
		if err != nil {
			return nil, err
		}
		for _, taskEntry := range taskEntries {
			if !taskEntry.IsDir() || !safeID(taskEntry.Name()) {
				continue
			}
			taskDir := filepath.Join(taskRoot, taskEntry.Name())
			prompt, err := os.ReadFile(filepath.Join(taskDir, "prompt.txt"))
			if err != nil {
				continue
			}
			_, fixtureErr := os.Stat(filepath.Join(taskDir, "fixture"))
			if fixtureErr != nil {
				continue
			}
			m := meta[taskEntry.Name()]
			t := BenchmarkTask{ID: b.ID + "/" + taskEntry.Name(), BenchmarkID: b.ID, TaskID: taskEntry.Name(), Name: m.Name, Prompt: string(prompt), Provenance: b.Provenance, Evaluator: b.Evaluator, TokenBudget: m.TokenBudget, HasFixture: true}
			if t.Name == "" {
				t.Name = t.TaskID
			}
			if len(m.Provenance) != 0 {
				t.Provenance = m.Provenance
			}
			if len(m.Evaluator) != 0 {
				t.Evaluator = m.Evaluator
			}
			b.Tasks = append(b.Tasks, t)
		}
		sort.Slice(b.Tasks, func(i, j int) bool { return b.Tasks[i].ID < b.Tasks[j].ID })
		// The catalog advertises only tasks that have a runnable on-disk capsule.
		// A stale manifest count must not cause the selection UI to over-promise.
		b.TaskCount = len(b.Tasks)
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Server) cases(w http.ResponseWriter, _ *http.Request) {
	out, err := s.caseList()
	if err != nil {
		apiError(w, 500, err)
		return
	}
	jsonOut(w, 200, out)
}

type batchRequest struct {
	VariantIDs       []string `json:"variant_ids"`
	HarnessIDs       []string `json:"harness_ids"`
	CaseIDs          []string `json:"case_ids"`
	BenchmarkTaskIDs []string `json:"benchmark_task_ids"`
	Workers          int      `json:"workers"`
	Model            string   `json:"model"`
	ReasoningEffort  string   `json:"reasoning_effort"`
}

func (s *Server) createBatch(w http.ResponseWriter, r *http.Request) {
	var req batchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		apiError(w, 400, err)
		return
	}
	if len(req.VariantIDs) == 0 {
		req.VariantIDs = req.HarnessIDs
	}
	if len(req.VariantIDs) == 0 || len(req.CaseIDs)+len(req.BenchmarkTaskIDs) == 0 {
		apiError(w, 400, errors.New("select at least one harness and case or benchmark task"))
		return
	}
	if hasDuplicates(req.VariantIDs) || hasDuplicates(req.CaseIDs) || hasDuplicates(req.BenchmarkTaskIDs) {
		apiError(w, 400, errors.New("harnesses, cases, and benchmark tasks must not contain duplicates"))
		return
	}
	selectedCount := len(req.CaseIDs) + len(req.BenchmarkTaskIDs)
	if len(req.VariantIDs) > maxBatchAttempts/selectedCount {
		apiError(w, 400, fmt.Errorf("a batch may contain at most %d attempts", maxBatchAttempts))
		return
	}
	if req.Workers < 1 {
		req.Workers = s.cfg.Workers
	}
	if req.Workers > 16 {
		req.Workers = 16
	}
	variants := map[string]run.Variant{}
	for _, id := range req.VariantIDs {
		if _, ok := variants[id]; ok {
			continue
		}
		v, err := s.cfg.LoadVariant(id)
		if err != nil {
			apiError(w, 400, fmt.Errorf("harness %q: %w", id, err))
			return
		}
		// Variants are immutable on disk. The selected settings apply only to this
		// batch's private execution copy and are validated by the same contract as manifests.
		if req.Model != "" {
			v.Model = req.Model
		}
		if req.ReasoningEffort != "" {
			v.ReasoningEffort = req.ReasoningEffort
		}
		if err := v.Validate(); err != nil {
			apiError(w, 400, fmt.Errorf("harness %q configuration: %w", id, err))
			return
		}
		variants[id] = v
	}
	cases := map[string]Case{}
	list, err := s.caseList()
	if err != nil {
		apiError(w, 500, err)
		return
	}
	for _, c := range list {
		cases[c.ID] = c
	}
	for _, id := range req.CaseIDs {
		if _, ok := cases[id]; !ok {
			apiError(w, 400, fmt.Errorf("unknown case %q", id))
			return
		}
	}
	benchmarks, err := s.benchmarkList()
	if err != nil {
		apiError(w, 500, err)
		return
	}
	benchmarkTasks := map[string]BenchmarkTask{}
	for _, benchmark := range benchmarks {
		for _, task := range benchmark.Tasks {
			benchmarkTasks[task.ID] = task
		}
	}
	for _, id := range req.BenchmarkTaskIDs {
		if _, ok := benchmarkTasks[id]; !ok {
			apiError(w, 400, fmt.Errorf("unknown benchmark task %q", id))
			return
		}
	}
	b := &Batch{ID: newID(), CreatedAt: time.Now().UTC(), Status: "queued"}
	for _, cid := range req.CaseIDs {
		for _, vid := range req.VariantIDs {
			v := variants[vid]
			d := describeVariant(v)
			b.Attempts = append(b.Attempts, &Attempt{ID: newID(), BatchID: b.ID, CreatedAt: b.CreatedAt, CaseID: cid, VariantID: vid, DisplayName: d.DisplayName, HarnessKind: d.Kind, TrajectoryAvailable: d.TrajectoryAvailable, Model: v.Model, ReasoningEffort: v.ReasoningEffort, PluginMode: d.PluginMode, PluginRevision: d.PluginRevision, Status: "queued"})
		}
	}
	for _, tid := range req.BenchmarkTaskIDs {
		task := benchmarkTasks[tid]
		for _, vid := range req.VariantIDs {
			v := variants[vid]
			d := describeVariant(v)
			b.Attempts = append(b.Attempts, &Attempt{ID: newID(), BatchID: b.ID, CreatedAt: b.CreatedAt, CaseID: task.ID, BenchmarkID: task.BenchmarkID, BenchmarkTaskID: task.TaskID, Provenance: task.Provenance, Evaluator: task.Evaluator, VariantID: vid, DisplayName: d.DisplayName, HarnessKind: d.Kind, TrajectoryAvailable: d.TrajectoryAvailable, Model: v.Model, ReasoningEffort: v.ReasoningEffort, PluginMode: d.PluginMode, PluginRevision: d.PluginRevision, Status: "queued"})
		}
	}
	s.mu.Lock()
	s.batches[b.ID] = b
	s.mu.Unlock()
	s.persist(b)
	s.emit(Event{Type: "batch", BatchID: b.ID, At: time.Now().UTC()})
	response := cloneBatch(b)
	go s.runBatch(b.ID, variants, req.Workers)
	jsonOut(w, 201, response)
}
func hasDuplicates(values []string) bool {
	seen := map[string]struct{}{}
	for _, v := range values {
		if _, ok := seen[v]; ok {
			return true
		}
		seen[v] = struct{}{}
	}
	return false
}
func newID() string { b := make([]byte, 8); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func (s *Server) runBatch(id string, variants map[string]run.Variant, workers int) {
	s.mu.Lock()
	b := s.batches[id]
	b.Status = "running"
	s.mu.Unlock()
	s.persistByID(id)
	s.emit(Event{Type: "batch", BatchID: id, At: time.Now().UTC()})
	jobs := make(chan *Attempt)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				s.runAttempt(a, variants[a.VariantID])
			}
		}()
	}
	s.mu.RLock()
	attempts := append([]*Attempt(nil), b.Attempts...)
	s.mu.RUnlock()
	for _, a := range attempts {
		jobs <- a
	}
	close(jobs)
	wg.Wait()
	s.mu.Lock()
	b.Status = "completed"
	s.mu.Unlock()
	s.persistByID(id)
	s.emit(Event{Type: "batch", BatchID: id, At: time.Now().UTC()})
}
func (s *Server) runAttempt(a *Attempt, v run.Variant) {
	caseRoot := s.cfg.CasesDir
	caseID := a.CaseID
	var benchmarkDir string
	if a.BenchmarkID != "" {
		if !safeID(a.BenchmarkID) || !safeID(a.BenchmarkTaskID) || a.CaseID != a.BenchmarkID+"/"+a.BenchmarkTaskID {
			s.finishAttempt(a, nil, errors.New("invalid benchmark task reference"))
			return
		}
		var pathErr error
		benchmarkDir, pathErr = s.safeBenchmarkTaskDir(a.BenchmarkID, a.BenchmarkTaskID)
		if pathErr != nil {
			s.finishAttempt(a, nil, pathErr)
			return
		}
		caseRoot, caseID = benchmarkDir, "."
	}
	promptPath := filepath.Join(caseRoot, caseID, "prompt.txt")
	fixture := filepath.Join(caseRoot, caseID, "fixture")
	hidden := filepath.Join(caseRoot, caseID, "hidden")
	if benchmarkDir != "" {
		var pathErr error
		promptPath, pathErr = safeBenchmarkChild(benchmarkDir, "prompt.txt")
		if pathErr == nil {
			fixture, pathErr = safeBenchmarkChild(benchmarkDir, "fixture")
		}
		if pathErr != nil {
			s.finishAttempt(a, nil, pathErr)
			return
		}
		if candidate, e := safeBenchmarkChild(benchmarkDir, "hidden"); e == nil {
			hidden = candidate
		} else if _, statErr := os.Lstat(filepath.Join(benchmarkDir, "hidden")); statErr == nil {
			s.finishAttempt(a, nil, e)
			return
		}
	}
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		s.finishAttempt(a, nil, err)
		return
	}
	if _, err = os.Stat(fixture); err != nil {
		s.finishAttempt(a, nil, errors.New("case fixture is missing"))
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	a.Status = "running"
	a.StartedAt = &now
	s.mu.Unlock()
	s.persistByID(a.BatchID)
	s.emit(Event{Type: "attempt", BatchID: a.BatchID, Attempt: copyAttempt(a), At: now})
	_, hasHidden := os.Stat(hidden)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan run.Outcome, 1)
	go func() {
		done <- s.cfg.Execute(ctx, v, run.Request{Fixture: fixture, Hidden: hidden, Prompt: string(prompt), CaseID: a.CaseID, BenchmarkID: a.BenchmarkID, BenchmarkTaskID: a.BenchmarkTaskID, Provenance: string(a.Provenance), Evaluator: string(a.Evaluator), Coding: hasHidden == nil, ExportWorkspace: hasHidden != nil})
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case out := <-done:
			s.finishAttempt(a, &out, nil)
			return
		case t := <-tick.C:
			s.mu.Lock()
			a.ElapsedMillis = t.Sub(now).Milliseconds()
			s.mu.Unlock()
			s.emit(Event{Type: "progress", BatchID: a.BatchID, Attempt: copyAttempt(a), At: t})
		}
	}
}

// safeBenchmarkTaskDir rejects symlink escapes before a benchmark is handed to
// Docker. Local cases retain their existing import-time path protections.
func (s *Server) safeBenchmarkTaskDir(benchmarkID, taskID string) (string, error) {
	benchmarkRoot, err := filepath.EvalSymlinks(s.cfg.BenchmarksDir)
	if err != nil {
		return "", errors.New("benchmark directory is missing")
	}
	suite, err := filepath.EvalSymlinks(filepath.Join(benchmarkRoot, benchmarkID))
	if err != nil || !pathInside(benchmarkRoot, suite) {
		return "", errors.New("unsafe benchmark suite path")
	}
	root, err := filepath.EvalSymlinks(filepath.Join(suite, "tasks"))
	if err != nil || !pathInside(suite, root) {
		return "", errors.New("benchmark tasks directory is missing")
	}
	task, err := filepath.EvalSymlinks(filepath.Join(root, taskID))
	if err != nil || !pathInside(root, task) {
		return "", errors.New("unsafe benchmark task path")
	}
	info, err := os.Stat(task)
	if err != nil || !info.IsDir() {
		return "", errors.New("benchmark task is missing")
	}
	return task, nil
}

func safeBenchmarkChild(taskDir, name string) (string, error) {
	p, err := filepath.EvalSymlinks(filepath.Join(taskDir, name))
	if err != nil || !pathInside(taskDir, p) {
		return "", fmt.Errorf("unsafe benchmark %s path", name)
	}
	return p, nil
}
func (s *Server) finishAttempt(a *Attempt, out *run.Outcome, err error) {
	t := time.Now().UTC()
	s.mu.Lock()
	a.FinishedAt = &t
	if err != nil {
		a.Status = "failed"
		a.Error = err.Error()
	} else {
		r := out.Result
		a.Result = &r
		a.Verdict = r.Verdict
		if a.Verdict == "" && r.Status == model.StatusCompleted && r.VerifierPassed != nil {
			if *r.VerifierPassed {
				a.Verdict = "pass"
			} else {
				a.Verdict = "fail"
			}
		}
		a.Status = string(r.Status)
		a.ElapsedMillis = r.ElapsedMillis
		a.Artifact = out.Dir
	}
	s.mu.Unlock()
	s.persistByID(a.BatchID)
	s.emit(Event{Type: "attempt", BatchID: a.BatchID, Attempt: copyAttempt(a), At: t})
}
func copyAttempt(a *Attempt) *Attempt {
	c := *a
	if a.Result != nil {
		r := *a.Result
		c.Result = &r
	}
	return &c
}

func (s *Server) listBatches(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	out := make([]*Batch, 0, len(s.batches))
	for _, b := range s.batches {
		out = append(out, cloneBatch(b))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	jsonOut(w, 200, out)
}
func (s *Server) getBatch(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/batches/")
	if strings.HasPrefix(r.URL.Path, "/api/runs/") {
		id = strings.TrimPrefix(r.URL.Path, "/api/runs/")
	}
	if strings.HasSuffix(id, "/logs") {
		s.logs(w, r, strings.TrimSuffix(id, "/logs"))
		return
	}
	s.mu.RLock()
	b := s.batches[id]
	var out *Batch
	if b != nil {
		out = cloneBatch(b)
	}
	s.mu.RUnlock()
	if out == nil {
		apiError(w, 404, errors.New("batch not found"))
		return
	}
	jsonOut(w, 200, out)
}

// deleteBatch removes a completed historical run and its locally saved attempt
// artifacts. Active runs remain intact so workers cannot write into a deleted record.
func (s *Server) deleteBatch(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/batches/")
	if strings.HasPrefix(r.URL.Path, "/api/runs/") {
		id = strings.TrimPrefix(r.URL.Path, "/api/runs/")
	}
	if !safeID(id) {
		apiError(w, http.StatusNotFound, errors.New("batch not found"))
		return
	}
	s.mu.Lock()
	b := s.batches[id]
	if b == nil {
		s.mu.Unlock()
		apiError(w, http.StatusNotFound, errors.New("batch not found"))
		return
	}
	if b.Status == "queued" || b.Status == "running" {
		s.mu.Unlock()
		apiError(w, http.StatusConflict, errors.New("an active run cannot be deleted"))
		return
	}
	delete(s.batches, id)
	s.mu.Unlock()

	_ = os.Remove(filepath.Join(s.cfg.DataDir, id+".json"))
	for _, a := range b.Attempts {
		s.removeArtifact(a.Artifact)
	}
	s.emit(Event{Type: "batch-deleted", BatchID: id, At: time.Now().UTC()})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteBatches(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || len(request.IDs) == 0 || hasDuplicates(request.IDs) {
		apiError(w, http.StatusBadRequest, errors.New("provide one or more unique run ids"))
		return
	}
	s.mu.Lock()
	batches := make([]*Batch, 0, len(request.IDs))
	for _, id := range request.IDs {
		if !safeID(id) || s.batches[id] == nil {
			s.mu.Unlock()
			apiError(w, http.StatusNotFound, errors.New("batch not found"))
			return
		}
		if s.batches[id].Status == "queued" || s.batches[id].Status == "running" {
			s.mu.Unlock()
			apiError(w, http.StatusConflict, errors.New("an active run cannot be deleted"))
			return
		}
		batches = append(batches, s.batches[id])
	}
	for _, b := range batches {
		delete(s.batches, b.ID)
	}
	s.mu.Unlock()
	for _, b := range batches {
		_ = os.Remove(filepath.Join(s.cfg.DataDir, b.ID+".json"))
		for _, a := range b.Attempts {
			s.removeArtifact(a.Artifact)
		}
		s.emit(Event{Type: "batch-deleted", BatchID: b.ID, At: time.Now().UTC()})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeArtifact(path string) {
	if path == "" {
		return
	}
	root, err := filepath.Abs(s.cfg.ArtifactRoot)
	if err != nil {
		return
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	_ = os.RemoveAll(target)
}
func (s *Server) logs(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.RLock()
	b := s.batches[id]
	var lines []string
	if b != nil {
		for _, a := range b.Attempts {
			lines = append(lines, a.ID+" "+a.Status)
		}
	}
	s.mu.RUnlock()
	if b == nil {
		apiError(w, 404, errors.New("batch not found"))
		return
	}
	jsonOut(w, 200, map[string]any{"lines": lines})
}

// GET /api/attempts/{attempt_id}/trajectory returns the captured, sanitized
// adapter trajectory. It intentionally never serves the broader attempt directory.
func (s *Server) getAttemptArtifact(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/attempts/")
	if !strings.HasSuffix(path, "/trajectory") {
		apiError(w, http.StatusNotFound, errors.New("not found"))
		return
	}
	id := strings.TrimSuffix(path, "/trajectory")
	if !safeID(id) || strings.Contains(id, "/") {
		apiError(w, http.StatusNotFound, errors.New("attempt not found"))
		return
	}
	a := s.findAttempt(id)
	if a == nil {
		apiError(w, http.StatusNotFound, errors.New("attempt not found"))
		return
	}
	t, err := readAttemptTrajectory(a)
	if err != nil {
		trajectoryError(w, err)
		return
	}
	jsonOut(w, http.StatusOK, t)
}

type trajectory struct {
	SchemaVersion string            `json:"schema_version"`
	Source        string            `json:"source"`
	EventCount    int               `json:"event_count"`
	DurationMS    int64             `json:"duration_ms"`
	Events        []json.RawMessage `json:"events"`
	Summary       json.RawMessage   `json:"summary,omitempty"`
}

func (s *Server) findAttempt(id string) *Attempt {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.batches {
		for _, a := range b.Attempts {
			if a.ID == id {
				return copyAttempt(a)
			}
		}
	}
	return nil
}

func isDSH(a *Attempt) bool {
	return a.HarnessKind == "deepseek-harness" || strings.HasPrefix(a.VariantID, "dsh-")
}

func readAttemptTrajectory(a *Attempt) (trajectory, error) {
	if a.Status != string(model.StatusCompleted) || !isDSH(a) || !a.TrajectoryAvailable && a.HarnessKind != "" {
		return trajectory{}, errTrajectoryUnavailable
	}
	if a.Artifact == "" {
		return trajectory{}, os.ErrNotExist
	}
	base, err := filepath.Abs(filepath.Clean(a.Artifact))
	if err != nil {
		return trajectory{}, err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return trajectory{}, os.ErrNotExist
	}
	out, err := filepath.EvalSymlinks(filepath.Join(base, "out"))
	if err != nil || !pathInside(base, out) {
		return trajectory{}, os.ErrNotExist
	}
	target, err := filepath.EvalSymlinks(filepath.Join(out, "trajectory.json"))
	if err != nil || !pathInside(out, target) {
		return trajectory{}, os.ErrNotExist
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return trajectory{}, os.ErrNotExist
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return trajectory{}, err
	}
	var t trajectory
	if err := json.Unmarshal(b, &t); err != nil || t.SchemaVersion == "" || t.Source == "" {
		return trajectory{}, errors.New("invalid trajectory artifact")
	}
	var legacy struct {
		Summary struct {
			EventCount    int64 `json:"event_count"`
			ElapsedMillis int64 `json:"elapsed_millis"`
		} `json:"summary"`
	}
	_ = json.Unmarshal(b, &legacy)
	if t.EventCount != 0 && t.EventCount != len(t.Events) {
		return trajectory{}, errors.New("invalid trajectory event count")
	}
	if legacy.Summary.EventCount != 0 && legacy.Summary.EventCount != int64(len(t.Events)) {
		return trajectory{}, errors.New("invalid trajectory event count")
	}
	t.EventCount = len(t.Events)
	if t.DurationMS == 0 {
		t.DurationMS = legacy.Summary.ElapsedMillis
	}
	return t, nil
}

func pathInside(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

var errTrajectoryUnavailable = errors.New("trajectory is unavailable for this attempt")

func trajectoryError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		apiError(w, http.StatusNotFound, errors.New("trajectory not found"))
		return
	}
	if errors.Is(err, errTrajectoryUnavailable) {
		apiError(w, http.StatusConflict, err)
		return
	}
	apiError(w, http.StatusBadRequest, err)
}

// POST /api/trajectories/compare accepts {"attempt_ids":["left","right"]}.
// GET accepts ?left_attempt_id=...&right_attempt_id=.... Both return raw
// trajectory summaries plus behavioral, identity-aligned comparison rows.
func (s *Server) compareTrajectories(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if r.Method == http.MethodGet {
		left, right := r.URL.Query().Get("left_attempt_id"), r.URL.Query().Get("right_attempt_id")
		if left == "" {
			left = r.URL.Query().Get("left")
		}
		if right == "" {
			right = r.URL.Query().Get("right")
		}
		ids = []string{left, right}
	} else {
		var req struct {
			AttemptIDs []string `json:"attempt_ids"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		ids = req.AttemptIDs
	}
	if len(ids) != 2 || ids[0] == "" || ids[1] == "" || ids[0] == ids[1] || !safeID(ids[0]) || !safeID(ids[1]) {
		apiError(w, http.StatusBadRequest, errors.New("provide exactly two distinct attempt_ids"))
		return
	}
	left, right := s.findAttempt(ids[0]), s.findAttempt(ids[1])
	if left == nil || right == nil {
		apiError(w, http.StatusNotFound, errors.New("attempt not found"))
		return
	}
	if left.CaseID != right.CaseID {
		apiError(w, http.StatusBadRequest, errors.New("trajectories must be from the same case"))
		return
	}
	lt, err := readAttemptTrajectory(left)
	if err != nil {
		trajectoryError(w, err)
		return
	}
	rt, err := readAttemptTrajectory(right)
	if err != nil {
		trajectoryError(w, err)
		return
	}
	rows := alignBehavioralEvents(behavioralEvents(lt.Events), behavioralEvents(rt.Events))
	changed, leftOnly, rightOnly := 0, 0, 0
	for _, row := range rows {
		switch row["status"] {
		case "changed":
			changed++
		case "left_only":
			leftOnly++
		case "right_only":
			rightOnly++
		}
	}
	jsonOut(w, http.StatusOK, map[string]any{"left": trajectorySummary(left, lt), "right": trajectorySummary(right, rt), "left_raw_events": lt.Events, "right_raw_events": rt.Events, "rows": rows, "comparison": map[string]any{"behavioral_event_count": len(rows), "changed": changed, "left_only": leftOnly, "right_only": rightOnly, "different": changed + leftOnly + rightOnly, "summary": comparisonSummary(changed, leftOnly, rightOnly)}})
}

func comparisonSummary(changed, leftOnly, rightOnly int) string {
	if changed+leftOnly+rightOnly == 0 {
		return "No behavioral differences"
	}
	return fmt.Sprintf("%d behavioral difference(s): %d changed, %d left-only, %d right-only", changed+leftOnly+rightOnly, changed, leftOnly, rightOnly)
}

type behavioralEvent struct {
	raw      json.RawMessage
	index    int
	identity string
}

func behavioralEvents(events []json.RawMessage) []behavioralEvent {
	out := make([]behavioralEvent, 0, len(events))
	for i, raw := range events {
		if identity, ok := behavioralIdentity(raw); ok {
			out = append(out, behavioralEvent{raw: raw, index: i, identity: identity})
		}
	}
	return out
}

// behavioralIdentity removes transport plumbing (stream chunks, block endings,
// usage/finish records, and request metadata) before alignment. The original
// raw records remain available through each attempt's trajectory endpoint.
func behavioralIdentity(raw json.RawMessage) (string, bool) {
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil {
		return "", false
	}
	typ, category, label := strings.ToLower(fmt.Sprint(event["type"])), strings.ToLower(fmt.Sprint(event["category"])), strings.ToLower(fmt.Sprint(event["label"]))
	normalizedType := strings.NewReplacer("_", "/", "-", "/").Replace(typ)
	switch normalizedType {
	case "assistant/chunk", "assistant/block/end", "block/end", "usage", "finish", "assistant/finish", "request/header", "request/context":
		return "", false
	}
	if typ == "" {
		typ = category
	}
	if label != "" {
		return typ + "|" + label, true
	}
	return typ + "|" + category, typ != ""
}

func alignBehavioralEvents(left, right []behavioralEvent) []map[string]any {
	// A weighted LCS prefers exact semantic matches over merely matching event
	// identities. This avoids consuming a later exact repeated tool call as an
	// earlier changed call, while still pairing a sole changed identity.
	dp := make([][]int, len(left)+1)
	for i := range dp {
		dp[i] = make([]int, len(right)+1)
	}
	pairScore := func(l, r behavioralEvent) int {
		if l.identity != r.identity {
			return 0
		}
		if semanticEventEqual(l.raw, r.raw) {
			return 3
		}
		return 1
	}
	for i := len(left) - 1; i >= 0; i-- {
		for j := len(right) - 1; j >= 0; j-- {
			pair, down, across := pairScore(left[i], right[j])+dp[i+1][j+1], dp[i+1][j], dp[i][j+1]
			dp[i][j] = max(pair, max(down, across))
		}
	}
	rows := []map[string]any{}
	i, j := 0, 0
	add := func(status string, l *behavioralEvent, r *behavioralEvent) {
		row := map[string]any{"status": status, "same": status == "same"}
		if l != nil {
			row["left"], row["left_index"], row["left_type"] = l.raw, l.index, trajectoryEventType(l.raw)
		}
		if r != nil {
			row["right"], row["right_index"], row["right_type"] = r.raw, r.index, trajectoryEventType(r.raw)
		}
		if status == "changed" || status == "left_only" || status == "right_only" {
			row["differences"] = trajectoryEventDifferences(row["left"], row["right"])
		}
		rows = append(rows, row)
	}
	for i < len(left) || j < len(right) {
		if i < len(left) && j < len(right) && pairScore(left[i], right[j]) > 0 && pairScore(left[i], right[j])+dp[i+1][j+1] >= dp[i+1][j] && pairScore(left[i], right[j])+dp[i+1][j+1] >= dp[i][j+1] {
			status := "changed"
			if semanticEventEqual(left[i].raw, right[j].raw) {
				status = "same"
			}
			add(status, &left[i], &right[j])
			i++
			j++
		} else if j >= len(right) || (i < len(left) && dp[i+1][j] >= dp[i][j+1]) {
			add("left_only", &left[i], nil)
			i++
		} else {
			add("right_only", nil, &right[j])
			j++
		}
	}
	return rows
}

// trajectoryEventDifferences keeps the comparison useful to a human: it names
// the fields that changed after volatile bookkeeping has been removed. Values
// are deliberately returned as JSON so the browser can render strings and
// structured payloads without lossy formatting.
func trajectoryEventDifferences(left, right any) []map[string]any {
	var l, r any
	if raw, ok := left.(json.RawMessage); ok {
		_ = json.Unmarshal(semanticEventJSON(raw), &l)
	}
	if raw, ok := right.(json.RawMessage); ok {
		_ = json.Unmarshal(semanticEventJSON(raw), &r)
	}
	changes := []map[string]any{}
	collectTrajectoryDifferences("", l, r, &changes)
	return changes
}

func collectTrajectoryDifferences(path string, left, right any, changes *[]map[string]any) {
	lm, lok := left.(map[string]any)
	rm, rok := right.(map[string]any)
	if lok && rok {
		keys := map[string]bool{}
		for k := range lm {
			keys[k] = true
		}
		for k := range rm {
			keys[k] = true
		}
		orderedKeys := make([]string, 0, len(keys))
		for k := range keys {
			orderedKeys = append(orderedKeys, k)
		}
		sort.Strings(orderedKeys)
		for _, k := range orderedKeys {
			next := k
			if path != "" {
				next = path + "." + k
			}
			collectTrajectoryDifferences(next, lm[k], rm[k], changes)
		}
		return
	}
	lb, _ := json.Marshal(left)
	rb, _ := json.Marshal(right)
	if string(lb) != string(rb) {
		*changes = append(*changes, map[string]any{"field": path, "left": left, "right": right})
	}
}

// semanticEventEqual ignores only top-level adapter bookkeeping. Nested tool
// payloads remain intact so a real input/result change is never hidden.
func semanticEventEqual(left, right json.RawMessage) bool {
	l, r := semanticEventJSON(left), semanticEventJSON(right)
	return l != nil && r != nil && string(l) == string(r)
}

func semanticEventJSON(raw json.RawMessage) []byte {
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil {
		return nil
	}
	for _, key := range []string{"index", "sequence", "relative_ms", "timestamp", "time", "turn", "step", "usage_delta"} {
		delete(event, key)
	}
	canonical, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	return canonical
}
func trajectoryEventType(raw json.RawMessage) string {
	var v struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Type
}
func trajectorySummary(a *Attempt, t trajectory) map[string]any {
	return map[string]any{"attempt_id": a.ID, "case_id": a.CaseID, "status": a.Status, "duration_ms": t.DurationMS, "elapsed_millis": a.ElapsedMillis, "usage": resultUsage(a), "event_count": t.EventCount}
}
func resultUsage(a *Attempt) any {
	if a.Result == nil {
		return nil
	}
	return a.Result.Usage
}
func cloneBatch(b *Batch) *Batch {
	c := *b
	c.Attempts = make([]*Attempt, len(b.Attempts))
	for i, a := range b.Attempts {
		c.Attempts[i] = copyAttempt(a)
	}
	return &c
}
func (s *Server) emit(e Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		apiError(w, 500, errors.New("streaming unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan Event, 32)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subs, ch); s.mu.Unlock() }()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if id := r.URL.Query().Get("batch_id"); id != "" && e.BatchID != id {
				continue
			}
			b, _ := json.Marshal(e)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
			fl.Flush()
		}
	}
}

func (s *Server) persistByID(id string) {
	s.mu.RLock()
	b := s.batches[id]
	c := cloneBatch(b)
	s.mu.RUnlock()
	s.persist(c)
}
func (s *Server) persist(b *Batch) {
	if b == nil {
		return
	}
	data, err := json.Marshal(b)
	if err != nil {
		return
	}
	tmp := filepath.Join(s.cfg.DataDir, b.ID+".tmp")
	if os.WriteFile(tmp, data, 0600) == nil {
		_ = os.Rename(tmp, filepath.Join(s.cfg.DataDir, b.ID+".json"))
	}
}
func (s *Server) loadHistory() error {
	entries, err := os.ReadDir(s.cfg.DataDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, e.Name()))
		if err != nil {
			continue
		}
		var x Batch
		if json.Unmarshal(b, &x) == nil && x.ID != "" {
			for _, a := range x.Attempts {
				if a.CreatedAt.IsZero() {
					a.CreatedAt = x.CreatedAt
				}
				if a.Verdict == "" && a.Result != nil && a.Result.Status == model.StatusCompleted && a.Result.VerifierPassed != nil {
					if *a.Result.VerifierPassed {
						a.Verdict = "pass"
					} else {
						a.Verdict = "fail"
					}
				}
			}
			if x.Status == "running" || x.Status == "queued" {
				x.Status = "interrupted"
				for _, a := range x.Attempts {
					if a.Status == "running" || a.Status == "queued" {
						a.Status = "interrupted"
					}
				}
			}
			s.batches[x.ID] = &x
		}
	}
	return nil
}

func (s *Server) importCase(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		apiError(w, 400, err)
		return
	}
	id := r.FormValue("id")
	if id == "" {
		id = r.FormValue("name")
	}
	prompt := r.FormValue("prompt")
	if !safeID(id) || strings.TrimSpace(prompt) == "" || len(prompt) > 1<<20 {
		apiError(w, 400, errors.New("id and prompt are required (prompt max 1 MiB)"))
		return
	}
	dest := filepath.Join(s.cfg.CasesDir, id)
	if _, err := os.Stat(dest); err == nil {
		apiError(w, 409, errors.New("case already exists"))
		return
	}
	if err := os.MkdirAll(s.cfg.CasesDir, 0700); err != nil {
		apiError(w, 500, err)
		return
	}
	stage, err := os.MkdirTemp(s.cfg.CasesDir, ".import-")
	if err != nil {
		apiError(w, 500, err)
		return
	}
	if err := os.MkdirAll(filepath.Join(stage, "fixture"), 0700); err != nil {
		_ = os.RemoveAll(stage)
		apiError(w, 500, err)
		return
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.WriteFile(filepath.Join(stage, "prompt.txt"), []byte(prompt), 0600); err != nil {
		apiError(w, 500, err)
		return
	}
	source := r.FormValue("source_type")
	archives := r.MultipartForm.File["archive"]
	uploads := r.MultipartForm.File["files"]
	fixture := filepath.Join(stage, "fixture")
	switch source {
	case "archive":
		if len(archives) != 1 || len(uploads) != 0 {
			apiError(w, 400, errors.New("archive import requires exactly one repository archive"))
			return
		}
		if err := extractUpload(archives[0], fixture); err != nil {
			apiError(w, 400, err)
			return
		}
		if err := flattenArchiveRoot(fixture, archives[0].Filename); err != nil {
			apiError(w, 400, err)
			return
		}
	case "repo_url":
		if len(archives) != 0 || len(uploads) != 0 {
			apiError(w, 400, errors.New("repository URL import cannot include uploaded files"))
			return
		}
		if err := cloneRepository(r.FormValue("repository"), fixture); err != nil {
			apiError(w, 400, err)
			return
		}
	case "files":
		if len(archives) != 0 || len(uploads) == 0 {
			apiError(w, 400, errors.New("file import requires at least one repository file"))
			return
		}
		var paths []string
		if raw := r.FormValue("file_paths"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &paths); err != nil {
				apiError(w, 400, errors.New("file_paths must be a JSON array"))
				return
			}
		}
		if len(paths) > 0 && len(paths) != len(uploads) {
			apiError(w, 400, errors.New("file_paths must align with files"))
			return
		}
		if len(uploads) > artifact.DefaultLimits().MaxFiles {
			apiError(w, 400, errors.New("too many files"))
			return
		}
		var total int64
		for i, fh := range uploads {
			path := fh.Filename
			if len(paths) > 0 {
				path = paths[i]
			}
			n, err := writeUpload(fh, fixture, path)
			total += n
			if err != nil || total > artifact.DefaultLimits().MaxTotalBytes {
				if err == nil {
					err = errors.New("uploaded files exceed size limit")
				}
				apiError(w, 400, err)
				return
			}
		}
	default:
		apiError(w, 400, errors.New("source_type must be archive, files, or repo_url"))
		return
	}
	if ok, err := hasRegularFile(fixture); err != nil || !ok {
		if err == nil {
			err = errors.New("repository source contains no files")
		}
		apiError(w, 400, err)
		return
	}
	if err := os.Rename(stage, dest); err != nil {
		apiError(w, 500, err)
		return
	}
	cleanup = false
	jsonOut(w, 201, Case{ID: id, Prompt: prompt, HasFixture: true})
}

func flattenArchiveRoot(root, archiveName string) error {
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() || !strings.EqualFold(entries[0].Name(), archiveBaseName(archiveName)) {
		return err
	}
	nested := filepath.Join(root, entries[0].Name())
	children, err := os.ReadDir(nested)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := os.Rename(filepath.Join(nested, child.Name()), filepath.Join(root, child.Name())); err != nil {
			return err
		}
	}
	return os.Remove(nested)
}

func archiveBaseName(name string) string {
	base := filepath.Base(name)
	lower := strings.ToLower(base)
	for _, suffix := range []string{".tar.gz", ".tar.gzip", ".tgz", ".zip", ".tar", ".gz"} {
		if strings.HasSuffix(lower, suffix) {
			return base[:len(base)-len(suffix)]
		}
	}
	return base
}

func hasRegularFile(root string) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			found = true
		}
		return nil
	})
	return found, err
}
func extractUpload(fh *multipart.FileHeader, dest string) error {
	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxUploadBytes {
		return errors.New("archive too large")
	}
	if z, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); err == nil {
		return extractZip(z, dest)
	}
	if gz, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
		defer gz.Close()
		return artifact.ExtractTar(gz, dest, artifact.DefaultLimits())
	}
	return artifact.ExtractTar(bytes.NewReader(b), dest, artifact.DefaultLimits())
}
func extractZip(z *zip.Reader, dest string) error {
	l := artifact.DefaultLimits()
	var total int64
	if len(z.File) > l.MaxFiles {
		return errors.New("archive exceeds entry limit")
	}
	for _, f := range z.File {
		n := filepath.ToSlash(f.Name)
		if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, "\\") || strings.Contains("/"+n+"/", "/../") || strings.HasPrefix(n, "../") {
			return errors.New("unsafe archive path")
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if f.FileInfo().Mode()&os.ModeType != 0 || f.UncompressedSize64 > uint64(l.MaxFileBytes) {
			return errors.New("unsafe or oversized archive entry")
		}
		total += int64(f.UncompressedSize64)
		if total > l.MaxTotalBytes {
			return errors.New("archive exceeds limits")
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(n))
		if err = os.MkdirAll(filepath.Dir(target), 0755); err == nil {
			out, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if e == nil {
				_, e = io.Copy(out, rc)
				ce := out.Close()
				if e == nil {
					e = ce
				}
			}
			err = e
		}
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func writeUpload(fh *multipart.FileHeader, dest, name string) (int64, error) {
	name = filepath.ToSlash(name)
	if name == "." || name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") || strings.Contains("/"+name+"/", "/../") || filepath.ToSlash(filepath.Clean(name)) != name {
		return 0, errors.New("unsafe uploaded file path")
	}
	in, err := fh.Open()
	if err != nil {
		return 0, err
	}
	defer in.Close()
	target := filepath.Join(dest, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, artifact.DefaultLimits().MaxFileBytes+1))
	cerr := out.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > artifact.DefaultLimits().MaxFileBytes {
		err = errors.New("uploaded file exceeds size limit")
	}
	return n, err
}
func cloneRepository(raw, dest string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("repository must be a credential-free HTTPS URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "protocol.file.allow=never", "-c", "core.hooksPath=/dev/null", "clone", "--depth", "1", "--no-tags", "--no-recurse-submodules", u.String(), dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = out
		if ctx.Err() != nil {
			return errors.New("repository clone timed out")
		}
		return errors.New("repository clone failed")
	}
	if err := os.RemoveAll(filepath.Join(dest, ".git")); err != nil {
		return err
	}
	var b bytes.Buffer
	return artifact.ArchiveDir(dest, &b, artifact.DefaultLimits())
}
