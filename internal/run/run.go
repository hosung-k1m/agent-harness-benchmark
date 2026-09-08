// Package run orchestrates a single disposable benchmark attempt.
package run

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hosungkim/agent-harness-benchmark/internal/artifact"
	"github.com/hosungkim/agent-harness-benchmark/internal/auth"
	"github.com/hosungkim/agent-harness-benchmark/internal/dockerx"
	"github.com/hosungkim/agent-harness-benchmark/internal/manifest"
	"github.com/hosungkim/agent-harness-benchmark/internal/model"
	"github.com/hosungkim/agent-harness-benchmark/internal/verify"
)

type Limits struct {
	CPUs        float64 `json:"cpus"`
	MemoryBytes int64   `json:"memory_bytes"`
	TmpfsBytes  int64   `json:"tmpfs_bytes"`
	PIDs        int     `json:"pids"`
	WallSeconds int     `json:"wall_seconds"`
}
type Variant struct {
	manifest.Variant
	DisplayName string `json:"display_name"`
	Limits      Limits `json:"limits"`
}
type Request struct {
	Fixture, Prompt, Hidden string
	CaseID                  string
	// Benchmark metadata is carried with an attempt so artifact consumers can
	// distinguish imported benchmark work from locally authored cases.
	BenchmarkID, BenchmarkTaskID, Provenance, Evaluator string
	Trial                                               int
	Coding, ExportWorkspace                             bool
}
type Outcome struct {
	Result        model.Result
	Verifier      verify.Result
	Dir           string
	SeededSecrets [][]byte
}
type Runner struct {
	Engine       dockerx.Engine
	Verifier     verify.Runner
	ArtifactRoot string
	UID          int
	Credential   func(string) auth.Provider
}

func unique(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
func (r Runner) Run(ctx context.Context, v Variant, req Request) (out Outcome) {
	if err := v.Validate(); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	if r.Engine == nil {
		out.Result = failure(model.StatusInfrastructureInvalid, fmt.Errorf("docker engine unavailable"))
		return
	}
	if r.UID == 0 {
		r.UID = 10001
	}
	if r.ArtifactRoot == "" {
		r.ArtifactRoot = ".bench/runs"
	}
	if err := os.MkdirAll(r.ArtifactRoot, 0700); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	dir, err := os.MkdirTemp(r.ArtifactRoot, "attempt-")
	if err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	out.Dir = dir
	name := unique("bench-")
	network := unique("bench-net-")
	// Every path after resources exist passes through this defer, including timeout/copy errors.
	var cleanup []error
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if e := dockerx.RemoveContainer(cctx, r.Engine, name); e != nil {
			cleanup = append(cleanup, e)
		}
		if e := dockerx.NetworkRemove(cctx, r.Engine, network); e != nil {
			cleanup = append(cleanup, e)
		}
		if len(cleanup) > 0 && out.Result.Status != model.StatusInfrastructureInvalid {
			out.Result.Status = model.StatusInfrastructureInvalid
			out.Result.FailureReason = "Docker cleanup failed"
		}
		annotate(&out.Result, filepath.Base(dir), v, req)
		_ = writeJSON(filepath.Join(dir, "result.json"), out.Result)
	}()
	if err = dockerx.NetworkCreate(ctx, r.Engine, network); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	if err = dockerx.CreateInert(ctx, r.Engine, name, network, v.Image, v.Limits.CPUs, v.Limits.MemoryBytes, v.Limits.TmpfsBytes, v.Limits.PIDs, r.UID); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	if err = dockerx.Start(ctx, r.Engine, name); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	// These are beneath uid-owned tmpfs mounts. Creating them before streamed
	// tar files avoids requiring tar to create parent paths through read-only /.
	if _, err = dockerx.Exec(ctx, r.Engine, name, "mkdir", "-p", "/home/bench/.codex", "/home/bench/.dsh", "/tmp/out"); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	fixtureTar, err := archiveDir(req.Fixture)
	if err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	if err = dockerx.ExtractTar(ctx, r.Engine, name, "/workspace", fixtureTar); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	p := r.Credential
	if p == nil {
		p = defaultCredential
	}
	materials, err := p(v.ID).Material()
	if err != nil {
		out.Result = failure(model.StatusUnsupported, err)
		return
	}
	for _, m := range materials {
		out.SeededSecrets = append(out.SeededSecrets, m.Data)
		out.SeededSecrets = append(out.SeededSecrets, m.Secrets...)
		e := putFile(ctx, r.Engine, name, m.Target, m.Data)
		if e != nil {
			out.Result = failure(model.StatusInfrastructureInvalid, e)
			return
		}
	}
	// Prompt is copied as data, never interpolated into a shell command.
	if err = putFile(ctx, r.Engine, name, "/tmp/prompt.txt", []byte(req.Prompt)); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	actx, cancel := context.WithTimeout(ctx, time.Duration(v.Limits.WallSeconds+10)*time.Second)
	defer cancel()
	adapterArgs := []string{v.Adapter, "/workspace", "/tmp/prompt.txt", "/home/bench", fmt.Sprint(v.Limits.WallSeconds), "/tmp/out/result.json", "/tmp/out/final.md", v.Model, v.ReasoningEffort}
	if v.ID == "dsh-modified-codex" {
		adapterArgs = append(adapterArgs, "dsh-modified-codex")
	}
	_, err = dockerx.Exec(actx, r.Engine, name, adapterArgs...)
	if actx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		out.Result = failure(model.StatusTimedOut, fmt.Errorf("wall timeout"))
		return
	}
	// Adapters intentionally exit zero after capturing agent status. Any failure here is infrastructure.
	if err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	outTar, copyErr := dockerx.CopyOut(ctx, r.Engine, name, "/tmp/out")
	if copyErr != nil {
		err = copyErr
	} else {
		err = artifact.ExtractTar(bytes.NewReader(outTar), filepath.Join(dir, "out"), artifact.DefaultLimits())
	}
	if err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "out", "result.json"))
	if err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	if err = json.Unmarshal(b, &out.Result); err != nil || out.Result.Validate() != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, fmt.Errorf("invalid adapter result"))
		return
	}
	annotate(&out.Result, filepath.Base(dir), v, req)
	secretPaths := []string{filepath.Join(dir, "out", "result.json"), filepath.Join(dir, "out", "final.md")}
	if _, statErr := os.Stat(filepath.Join(dir, "out", "trajectory.json")); statErr == nil {
		secretPaths = append(secretPaths, filepath.Join(dir, "out", "trajectory.json"))
	}
	if err = artifact.RejectSecrets(secretPaths, out.SeededSecrets); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	if req.Coding || req.ExportWorkspace {
		r.exportWorkspace(ctx, name, &out)
	}
	if req.Coding && out.Result.Status != model.StatusInfrastructureInvalid {
		r.verify(ctx, name, v, req, &out)
	}
	return
}

func annotate(result *model.Result, runID string, v Variant, req Request) {
	result.SchemaVersion = "1"
	result.RunID = runID
	result.CaseID = req.CaseID
	result.BenchmarkID = req.BenchmarkID
	result.BenchmarkTaskID = req.BenchmarkTaskID
	result.Provenance = metadataJSON(req.Provenance)
	result.Evaluator = metadataJSON(req.Evaluator)
	result.VariantID = v.ID
	result.Trial = req.Trial
	result.Model = v.Model
	result.ReasoningEffort = v.ReasoningEffort
	result.NetworkPolicyID = v.NetworkPolicyID
}
func metadataJSON(value string) json.RawMessage {
	if value == "" {
		return nil
	}
	data := json.RawMessage(value)
	if json.Valid(data) {
		return append(json.RawMessage(nil), data...)
	}
	// Request metadata is normally serialized JSON from the catalog. Encoding
	// unexpected values as strings keeps result.json valid and durable.
	encoded, _ := json.Marshal(value)
	return encoded
}
func defaultCredential(id string) auth.Provider {
	if id == "dsh-default-codex" || id == "dsh-modified-codex" {
		return auth.DSHCodex{}
	}
	return auth.Codex{}
}
func archiveDir(dir string) ([]byte, error) {
	var b bytes.Buffer
	if err := artifact.ArchiveDir(dir, &b, artifact.DefaultLimits()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func putFile(ctx context.Context, e dockerx.Engine, container, target string, data []byte) error {
	name := strings.TrimPrefix(filepath.ToSlash(target), "/")
	if name == "" || strings.Contains(name, "../") {
		return fmt.Errorf("unsafe container target")
	}
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return dockerx.ExtractTar(ctx, e, container, "/", b.Bytes())
}
func (r Runner) exportWorkspace(ctx context.Context, name string, out *Outcome) {
	work := filepath.Join(out.Dir, "workspace")
	if err := os.MkdirAll(work, 0700); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	b, err := dockerx.CopyOut(ctx, r.Engine, name, "/workspace")
	if err == nil {
		err = artifact.ExtractTar(bytes.NewReader(b), work, artifact.DefaultLimits())
	}
	if err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
	}
}
func (r Runner) verify(ctx context.Context, name string, v Variant, req Request, out *Outcome) {
	work := filepath.Join(out.Dir, "workspace")
	if _, err := os.Stat(work); err != nil {
		r.exportWorkspace(ctx, name, out)
		if out.Result.Status == model.StatusInfrastructureInvalid {
			return
		}
	}
	archive := filepath.Join(out.Dir, "workspace.tar")
	f, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		err = artifact.ArchiveDir(work, f, artifact.DefaultLimits())
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
	}
	if err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	if err = artifact.RejectSecrets([]string{archive}, out.SeededSecrets); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	// Only the sanitized archive crosses the durable attempt boundary.
	defer os.RemoveAll(work)
	if r.Verifier == nil {
		r.Verifier = verify.Docker{Engine: r.Engine}
	}
	vr, err := r.Verifier.Verify(ctx, archive, req.Hidden, filepath.Join(out.Dir, "verifier"))
	if err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	if err = artifact.RejectSecrets([]string{filepath.Join(out.Dir, "verifier", "result.json")}, out.SeededSecrets); err != nil {
		out.Result = failure(model.StatusInfrastructureInvalid, err)
		return
	}
	out.Verifier = vr
	out.Result.VerifierPassed = &vr.VerifierPassed
	if vr.Status == string(model.StatusInfrastructureInvalid) {
		out.Result.Status = model.StatusInfrastructureInvalid
		out.Result.FailureReason = vr.Reason
	} else if vr.VerifierPassed {
		out.Result.Verdict = "pass"
	} else {
		out.Result.Verdict = "fail"
	}
	_ = writeJSON(filepath.Join(out.Dir, "verifier-result.json"), vr)
}
func failure(s model.Status, e error) model.Result {
	return model.Result{Status: s, Usage: model.Usage{TokenQuality: model.TokenUnavailable}, FailureReason: clean(e)}
}
func clean(e error) string {
	if e == nil {
		return ""
	}
	return strings.ReplaceAll(strings.ReplaceAll(e.Error(), "\n", " "), "\r", " ")
}
func writeJSON(p string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(p, append(b, '\n'), 0600)
}
