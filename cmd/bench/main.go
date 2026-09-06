// bench runs the intentionally small, Docker-isolated benchmark.
package main

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hosungkim/agent-harness-benchmark/internal/compare"
	"github.com/hosungkim/agent-harness-benchmark/internal/dockerx"
	"github.com/hosungkim/agent-harness-benchmark/internal/manifest"
	"github.com/hosungkim/agent-harness-benchmark/internal/model"
	"github.com/hosungkim/agent-harness-benchmark/internal/run"
	"github.com/hosungkim/agent-harness-benchmark/internal/state"
	benchweb "github.com/hosungkim/agent-harness-benchmark/internal/web"
)

const root = ".bench"

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}
func mainErr() error {
	if len(os.Args) < 2 {
		return errors.New("usage: bench preflight|smoke|run|compare|serve")
	}
	switch os.Args[1] {
	case "preflight":
		return preflight(os.Args[2:])
	case "smoke":
		return smoke(os.Args[2:])
	case "run":
		return oneRun(os.Args[2:])
	case "compare":
		return doCompare(os.Args[2:])
	case "serve":
		return serve(os.Args[2:])
	default:
		return errors.New("unknown command")
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address (loopback by default)")
	workers := fs.Int("workers", 2, "maximum concurrent attempts")
	static := fs.String("static", "internal/web/static", "directory containing dashboard assets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := benchweb.New(benchweb.Config{
		CasesDir: "cases", DataDir: filepath.Join(root, "batches"), Workers: *workers,
		LoadVariant: variant,
		Execute: func(ctx context.Context, v run.Variant, req run.Request) run.Outcome {
			return runner().Run(ctx, v, req)
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("benchmark dashboard listening on http://%s\n", *addr)
	return http.ListenAndServe(*addr, s.Handler(*static))
}
func variant(id string) (run.Variant, error) {
	b, e := os.ReadFile(filepath.Join("variants", id+".json"))
	if e != nil {
		return run.Variant{}, e
	}
	var v run.Variant
	if e = json.Unmarshal(b, &v); e != nil {
		return v, e
	}
	if v.ID != id {
		return v, errors.New("variant id mismatch")
	}
	out, e := dockerx.CLI{}.Run(context.Background(), "image", "inspect", "--format", "{{.Id}}", v.Image)
	if e != nil {
		return v, fmt.Errorf("image must be built/available: %w", e)
	}
	v.ImageDigest = strings.TrimSpace(string(out))
	if e = v.Validate(); e != nil {
		return v, e
	}
	if e = verifyImageAdapter(v); e != nil {
		return v, e
	}
	return v, nil
}

func adapterSources(v run.Variant) []string {
	paths := []string{
		filepath.Join("docker", "adapters", filepath.Base(v.Adapter)),
		filepath.Join("docker", "adapters", "normalize.py"),
	}
	if v.ID == "dsh-default-codex" {
		paths = append(paths, filepath.Join("docker", "adapters", "dsh-bench.patch.yml"))
	}
	return paths
}

func fileDigest(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:]), nil
}

func verifyImageAdapter(v run.Variant) error {
	var imagePaths []string
	for _, path := range adapterSources(v) {
		imagePaths = append(imagePaths, filepath.ToSlash(filepath.Join("/opt/bench", filepath.Base(path))))
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "sha256sum", v.Image}
	out, err := (dockerx.CLI{}).Run(context.Background(), append(args, imagePaths...)...)
	if err != nil {
		return fmt.Errorf("inspect adapter in image: %w", err)
	}
	lines := strings.Fields(string(out))
	if len(lines) != len(imagePaths)*2 {
		return errors.New("unexpected adapter digest output from image")
	}
	for i, source := range adapterSources(v) {
		want, err := fileDigest(source)
		if err != nil {
			return fmt.Errorf("read adapter source: %w", err)
		}
		if lines[i*2] != want {
			return fmt.Errorf("benchmark image is stale for %s; rebuild it", filepath.Base(source))
		}
	}
	return nil
}

func hash(v run.Variant) (string, error) {
	h, err := manifest.Hash(v.Variant)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	digest.Write([]byte(h))
	for _, source := range adapterSources(v) {
		b, err := os.ReadFile(source)
		if err != nil {
			return "", fmt.Errorf("read adapter for state hash: %w", err)
		}
		digest.Write([]byte(filepath.Base(source)))
		digest.Write(b)
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}
func runner() run.Runner {
	return run.Runner{Engine: dockerx.CLI{}, ArtifactRoot: filepath.Join(root, "runs")}
}
func saveState(kind string, v run.Variant) error {
	h, e := hash(v)
	if e != nil {
		return e
	}
	if state.Current(root, kind, v.ID, h) {
		return nil
	}
	return state.Write(root, state.Record{Kind: kind, VariantID: v.ID, ConfigHash: h, CreatedAt: now(), Success: true})
}
func current(kind string, v run.Variant) bool {
	h, e := hash(v)
	return e == nil && state.Current(root, kind, v.ID, h)
}
func now() time.Time { return time.Now().UTC() }

func preflight(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	id := fs.String("variant", "", "variant id")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *id == "" {
		return errors.New("--variant is required")
	}
	v, e := variant(*id)
	if e != nil {
		return e
	}
	d, e := os.MkdirTemp("", "bench-preflight-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(d)
	nonce := "bench-" + fmt.Sprint(time.Now().UnixNano())
	prompt := "In the workspace, create a file named " + nonce + " containing exactly " + nonce + ". Do not do anything else."
	o := runner().Run(context.Background(), v, run.Request{Fixture: d, Prompt: prompt, CaseID: "preflight", ExportWorkspace: true})
	if o.Result.Status != model.StatusCompleted {
		return fmt.Errorf("preflight %s: %s", o.Result.Status, o.Result.FailureReason)
	}
	b, e := os.ReadFile(filepath.Join(o.Dir, "workspace", nonce))
	defer os.RemoveAll(filepath.Join(o.Dir, "workspace"))
	if e != nil || strings.TrimSpace(string(b)) != nonce {
		return errors.New("preflight sentinel edit was not independently verified")
	}
	if o.Result.Usage.TokenQuality != model.TokenProviderReported || o.Result.Usage.InputTokens == nil || o.Result.Usage.OutputTokens == nil {
		return errors.New("preflight did not extract authoritative provider usage")
	}
	if e = saveState("preflight", v); e != nil {
		return e
	}
	fmt.Println("preflight passed", v.ID)
	return nil
}

const smokePrompt = "Summarize this repository: identify its purpose, main directories, primary\nlanguages/frameworks, and the most relevant entry points. Return a concise\nMarkdown summary."

func smoke(args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	id := fs.String("variant", "", "variant id")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *id == "" {
		return errors.New("--variant is required")
	}
	v, e := variant(*id)
	if e != nil {
		return e
	}
	if !current("preflight", v) {
		return errors.New("current successful preflight is required")
	}
	o := runner().Run(context.Background(), v, run.Request{Fixture: "cases/smoke-repository", Prompt: smokePrompt, CaseID: "smoke-repository"})
	if o.Result.Status != model.StatusCompleted {
		return fmt.Errorf("smoke %s: %s", o.Result.Status, o.Result.FailureReason)
	}
	b, e := os.ReadFile(filepath.Join(o.Dir, "out", "final.md"))
	if e != nil {
		return e
	}
	s := strings.ToLower(string(b))
	for _, word := range []string{"purpose", "director", "language", "entry"} {
		if !strings.Contains(s, word) {
			return fmt.Errorf("smoke summary missing %s", word)
		}
	}
	if e = saveState("smoke", v); e != nil {
		return e
	}
	fmt.Println("smoke passed", v.ID)
	return nil
}
func oneRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cid := fs.String("case", "", "case id")
	id := fs.String("variant", "", "variant id")
	trial := fs.Int("trial", 0, "trial")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *cid != "slugify-v1" || *id == "" || *trial < 1 {
		return errors.New("--case slugify-v1, --variant and --trial >= 1 are required")
	}
	v, e := variant(*id)
	if e != nil {
		return e
	}
	p, e := os.ReadFile("cases/slugify-v1/prompt.txt")
	if e != nil {
		return e
	}
	o := runner().Run(context.Background(), v, run.Request{Fixture: "cases/slugify-v1/fixture", Hidden: "cases/slugify-v1/hidden", Prompt: string(p), CaseID: *cid, Trial: *trial, Coding: true})
	fmt.Printf("%s trial %d: %s (%s)\nartifacts: %s\n", v.ID, *trial, o.Result.Status, o.Result.FailureReason, o.Dir)
	return nil
}

type plan struct {
	Case     string        `json:"case"`
	Trials   int           `json:"trials"`
	Seed     int64         `json:"seed"`
	Orders   [][]string    `json:"orders"`
	Attempts []planAttempt `json:"attempts"`
}

type planAttempt struct {
	Trial    int    `json:"trial"`
	Variant  string `json:"variant"`
	Artifact string `json:"result_artifact"`
}

func writePlan(path string, pl plan) error {
	b, err := json.MarshalIndent(pl, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func doCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	cid := fs.String("case", "", "case id")
	trials := fs.Int("trials", 0, "number of trials")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *cid != "slugify-v1" || *trials < 1 {
		return errors.New("--case slugify-v1 and --trials >= 1 are required")
	}
	vs := make([]run.Variant, 2)
	ids := []string{"codex-cli", "dsh-default-codex"}
	for i, id := range ids {
		v, e := variant(id)
		if e != nil {
			return e
		}
		if !current("preflight", v) || !current("smoke", v) {
			return fmt.Errorf("current preflight and smoke are required for %s", id)
		}
		vs[i] = v
	}
	var raw [8]byte
	if _, e := crand.Read(raw[:]); e != nil {
		return e
	}
	seed := int64(binary.LittleEndian.Uint64(raw[:]))
	pl := plan{Case: *cid, Trials: *trials, Seed: seed}
	for n := 0; n < *trials; n++ {
		pl.Orders = append(pl.Orders, compare.Order(ids, seed+int64(n)))
	}
	if e := os.MkdirAll(filepath.Join(root, "compare"), 0700); e != nil {
		return e
	}
	planPath := filepath.Join(root, "compare", fmt.Sprintf("%d-plan.json", seed))
	if e := writePlan(planPath, pl); e != nil {
		return e
	}
	sums := map[string]*compare.Summary{}
	for _, v := range vs {
		sums[v.ID] = &compare.Summary{Label: v.DisplayName}
	}
	prompt, e := os.ReadFile("cases/slugify-v1/prompt.txt")
	if e != nil {
		return e
	}
	for trialIndex, order := range pl.Orders {
		for _, id := range order {
			var v run.Variant
			for _, x := range vs {
				if x.ID == id {
					v = x
				}
			}
			o := runner().Run(context.Background(), v, run.Request{Fixture: "cases/slugify-v1/fixture", Hidden: "cases/slugify-v1/hidden", Prompt: string(prompt), CaseID: *cid, Trial: trialIndex + 1, Coding: true})
			// Summary derivation deliberately reads the persisted normalized artifact.
			resultPath := filepath.Join(o.Dir, "result.json")
			b, readErr := os.ReadFile(resultPath)
			var stored model.Result
			if readErr != nil || json.Unmarshal(b, &stored) != nil {
				return fmt.Errorf("read stored result: %w", readErr)
			}
			sums[id].Results = append(sums[id].Results, stored)
			rel, relErr := filepath.Rel(filepath.Join(root, "compare"), resultPath)
			if relErr != nil {
				return relErr
			}
			pl.Attempts = append(pl.Attempts, planAttempt{Trial: trialIndex + 1, Variant: id, Artifact: rel})
			if e := writePlan(planPath, pl); e != nil {
				return e
			}
		}
	}
	text := compare.RenderMarkdown(*trials, []compare.Summary{*sums[ids[0]], *sums[ids[1]]})
	fmt.Print(text)
	return os.WriteFile(filepath.Join(root, "compare", fmt.Sprintf("%d-summary.md", seed)), []byte(text), 0600)
}
