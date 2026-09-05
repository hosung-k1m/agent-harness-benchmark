package run

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hosungkim/agent-harness-benchmark/internal/auth"
	"github.com/hosungkim/agent-harness-benchmark/internal/manifest"
	"github.com/hosungkim/agent-harness-benchmark/internal/model"
)

func TestLimitsDecodeSnakeCase(t *testing.T) {
	var v Variant
	if err := json.Unmarshal([]byte(`{"id":"codex-cli","image":"x","image_digest":"sha256:x","adapter":"a","model":"gpt-5.6-luna","reasoning_effort":"low","network_policy_id":"unrestricted-egress-v1","limits":{"cpus":2,"memory_bytes":4,"tmpfs_bytes":5,"pids":6,"wall_seconds":7}}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Limits.MemoryBytes != 4 || v.Limits.TmpfsBytes != 5 || v.Limits.PIDs != 6 || v.Limits.WallSeconds != 7 {
		t.Fatalf("limits not decoded: %#v", v.Limits)
	}
}

type fakeEngine struct {
	calls   [][]string
	inputs  int
	execErr error
	result  model.Result
}

func (f *fakeEngine) RunInput(_ context.Context, _ []byte, a ...string) ([]byte, error) {
	f.inputs++
	f.calls = append(f.calls, append([]string(nil), a...))
	return nil, nil
}

func (f *fakeEngine) Run(_ context.Context, a ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), a...))
	if len(a) > 0 && a[0] == "exec" && strings.Contains(strings.Join(a, " "), "/opt/") {
		return nil, f.execErr
	}
	if len(a) > 0 && a[0] == "exec" && strings.Contains(strings.Join(a, " "), "tar -cf - -C /tmp/out .") {
		b, _ := json.Marshal(f.result)
		return testTar(map[string][]byte{"result.json": b, "final.md": []byte("ok")}), nil
	}
	if len(a) > 0 && a[0] == "cp" && strings.Contains(a[1], ":/tmp/out") {
		d := a[2]
		_ = os.MkdirAll(filepath.Join(d, "out"), 0700)
		b, _ := json.Marshal(f.result)
		_ = os.WriteFile(filepath.Join(d, "out", "result.json"), b, 0600)
		_ = os.WriteFile(filepath.Join(d, "out", "final.md"), []byte("ok"), 0600)
	}
	return nil, nil
}
func testTar(files map[string][]byte) []byte {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for n, d := range files {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0600, Size: int64(len(d)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(d)
	}
	_ = tw.Close()
	return b.Bytes()
}
func (f *fakeEngine) called(prefix string) bool {
	for _, a := range f.calls {
		if len(a) > 1 && a[0]+" "+a[1] == prefix {
			return true
		}
	}
	return false
}

type fakeAuth struct{ err error }

func (a fakeAuth) Material() ([]auth.Material, error) { return nil, a.err }
func testVariant() Variant {
	return Variant{Variant: manifest.Variant{ID: "codex-cli", Image: "image", ImageDigest: "sha256:test", Adapter: "/opt/bench/codex", Model: manifest.Model, ReasoningEffort: manifest.Reasoning, NetworkPolicyID: manifest.NetworkPolicy}, Limits: Limits{CPUs: 1, MemoryBytes: 1, TmpfsBytes: 1, PIDs: 1, WallSeconds: 1}}
}
func TestCleanupWhenCredentialUnsupported(t *testing.T) {
	f := &fakeEngine{}
	r := Runner{Engine: f, ArtifactRoot: t.TempDir(), Credential: func(string) auth.Provider { return fakeAuth{errors.New("missing")} }}
	o := r.Run(context.Background(), testVariant(), Request{Fixture: t.TempDir()})
	if o.Result.Status != model.StatusUnsupported {
		t.Fatalf("got %s", o.Result.Status)
	}
	if !f.called("rm -f") || !f.called("network rm") {
		t.Fatalf("cleanup missing: %#v", f.calls)
	}
	if f.inputs == 0 {
		t.Fatal("fixture was not streamed into container")
	}
}

func TestCreatesMissingArtifactRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "runs")
	f := &fakeEngine{}
	r := Runner{Engine: f, ArtifactRoot: root, Credential: func(string) auth.Provider { return fakeAuth{errors.New("missing")} }}
	_ = r.Run(context.Background(), testVariant(), Request{Fixture: t.TempDir()})
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("artifact root was not created: %v", err)
	}
}
func TestTimeoutStillCleansUp(t *testing.T) {
	f := &fakeEngine{execErr: context.DeadlineExceeded}
	r := Runner{Engine: f, ArtifactRoot: t.TempDir(), Credential: func(string) auth.Provider { return fakeAuth{} }}
	o := r.Run(context.Background(), testVariant(), Request{Fixture: t.TempDir()})
	if o.Result.Status != model.StatusTimedOut {
		t.Fatalf("got %s: %s", o.Result.Status, o.Result.FailureReason)
	}
	if !f.called("rm -f") || !f.called("network rm") {
		t.Fatal("cleanup missing after timeout")
	}
}
func TestCollectsAdapterOutputThroughTarStream(t *testing.T) {
	f := &fakeEngine{result: model.Result{Status: model.StatusCompleted, Usage: model.Usage{TokenQuality: model.TokenUnavailable}}}
	r := Runner{Engine: f, ArtifactRoot: t.TempDir(), Credential: func(string) auth.Provider { return fakeAuth{} }}
	o := r.Run(context.Background(), testVariant(), Request{Fixture: t.TempDir()})
	if o.Result.Status != model.StatusCompleted {
		t.Fatalf("got %s: %s", o.Result.Status, o.Result.FailureReason)
	}
	if _, err := os.Stat(filepath.Join(o.Dir, "out", "result.json")); err != nil {
		t.Fatal(err)
	}
}
