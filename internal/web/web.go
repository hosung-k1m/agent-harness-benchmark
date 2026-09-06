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
type Harness struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}
type Attempt struct {
	ID            string        `json:"id"`
	BatchID       string        `json:"batch_id"`
	CaseID        string        `json:"case_id"`
	VariantID     string        `json:"variant_id"`
	Status        string        `json:"status"`
	StartedAt     *time.Time    `json:"started_at,omitempty"`
	FinishedAt    *time.Time    `json:"finished_at,omitempty"`
	ElapsedMillis int64         `json:"elapsed_millis"`
	Result        *model.Result `json:"result,omitempty"`
	Artifact      string        `json:"artifact,omitempty"`
	Error         string        `json:"error,omitempty"`
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
	CasesDir, DataDir string
	Workers           int
	LoadVariant       func(string) (run.Variant, error)
	Execute           func(context.Context, run.Variant, run.Request) run.Outcome
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
	if cfg.DataDir == "" {
		cfg.DataDir = ".bench/batches"
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
	mux.HandleFunc("GET /api/runs/", s.getBatch)
	mux.HandleFunc("GET /api/batches", s.listBatches)
	mux.HandleFunc("POST /api/batches", s.createBatch)
	mux.HandleFunc("GET /api/batches/", s.getBatch)
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
		out = append(out, Harness{v.ID, v.DisplayName, v.Model, v.ReasoningEffort})
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
		h = append(h, map[string]any{"id": v.ID, "name": v.DisplayName, "description": v.Model + " / " + v.ReasoningEffort, "available": true})
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
	jsonOut(w, 200, map[string]any{"harnesses": h, "cases": cs})
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
func (s *Server) cases(w http.ResponseWriter, _ *http.Request) {
	out, err := s.caseList()
	if err != nil {
		apiError(w, 500, err)
		return
	}
	jsonOut(w, 200, out)
}

type batchRequest struct {
	VariantIDs []string `json:"variant_ids"`
	HarnessIDs []string `json:"harness_ids"`
	CaseIDs    []string `json:"case_ids"`
	Workers    int      `json:"workers"`
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
	if len(req.VariantIDs) == 0 || len(req.CaseIDs) == 0 {
		apiError(w, 400, errors.New("select at least one harness and case"))
		return
	}
	if hasDuplicates(req.VariantIDs) || hasDuplicates(req.CaseIDs) {
		apiError(w, 400, errors.New("harnesses and cases must not contain duplicates"))
		return
	}
	if len(req.VariantIDs) > maxBatchAttempts/len(req.CaseIDs) {
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
	b := &Batch{ID: newID(), CreatedAt: time.Now().UTC(), Status: "queued"}
	for _, cid := range req.CaseIDs {
		for _, vid := range req.VariantIDs {
			b.Attempts = append(b.Attempts, &Attempt{ID: newID(), BatchID: b.ID, CaseID: cid, VariantID: vid, Status: "queued"})
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
	prompt, err := os.ReadFile(filepath.Join(s.cfg.CasesDir, a.CaseID, "prompt.txt"))
	if err != nil {
		s.finishAttempt(a, nil, err)
		return
	}
	fixture := filepath.Join(s.cfg.CasesDir, a.CaseID, "fixture")
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
	hidden := filepath.Join(s.cfg.CasesDir, a.CaseID, "hidden")
	_, hasHidden := os.Stat(hidden)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan run.Outcome, 1)
	go func() {
		done <- s.cfg.Execute(ctx, v, run.Request{Fixture: fixture, Hidden: hidden, Prompt: string(prompt), CaseID: a.CaseID, Coding: hasHidden == nil, ExportWorkspace: hasHidden != nil})
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
