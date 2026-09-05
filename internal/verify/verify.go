// Package verify invokes the trusted verifier in an isolated container.
package verify

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hosungkim/agent-harness-benchmark/internal/artifact"
	"github.com/hosungkim/agent-harness-benchmark/internal/dockerx"
)

type Result struct {
	Status         string   `json:"status"`
	VerifierPassed bool     `json:"verifier_passed"`
	Reason         string   `json:"reason,omitempty"`
	Logs           []string `json:"logs,omitempty"`
}
type Runner interface {
	Verify(context.Context, string, string, string) (Result, error)
}
type Docker struct {
	Engine dockerx.Engine
	Image  string
}

func (d Docker) Verify(ctx context.Context, archive, hidden, outDir string) (res Result, err error) {
	if d.Image == "" {
		d.Image = "agent-harness-verifier:latest"
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return Result{}, err
	}
	name := unique()
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if e := dockerx.RemoveContainer(c, d.Engine, name); e != nil && err == nil {
			err = fmt.Errorf("verifier cleanup: %w", e)
		}
	}()
	tmp := "rw,nosuid,nodev,uid=10001,gid=10001,mode=0700,size="
	_, e := d.Engine.Run(ctx, "create", "--name", name, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--tmpfs", "/input:"+tmp+"192m", "--tmpfs", "/hidden:"+tmp+"32m", "--tmpfs", "/output:"+tmp+"16m", "--tmpfs", "/tmp:"+tmp+"64m", "--entrypoint", "sleep", d.Image, "infinity")
	if e != nil {
		return Result{}, fmt.Errorf("create verifier: %w", e)
	}
	if e = dockerx.Start(ctx, d.Engine, name); e != nil {
		return Result{}, e
	}
	b, e := os.ReadFile(archive)
	if e != nil {
		return Result{}, e
	}
	if e = putFile(ctx, d.Engine, name, "/input/workspace.tar", b); e != nil {
		return Result{}, e
	}
	var hb bytes.Buffer
	if e = artifact.ArchiveDir(hidden, &hb, artifact.DefaultLimits()); e != nil {
		return Result{}, e
	}
	if e = dockerx.ExtractTar(ctx, d.Engine, name, "/hidden", hb.Bytes()); e != nil {
		return Result{}, e
	}
	if _, e = dockerx.Exec(ctx, d.Engine, name, "python3", "/opt/verifier/verify.py", "/input/workspace.tar", "/hidden", "/output/result.json"); e != nil {
		return Result{}, fmt.Errorf("trusted verifier: %w", e)
	}
	outputTar, copyErr := dockerx.CopyOut(ctx, d.Engine, name, "/output")
	if copyErr != nil {
		e = copyErr
	} else {
		e = artifact.ExtractTar(bytes.NewReader(outputTar), outDir, artifact.DefaultLimits())
	}
	if e != nil {
		return Result{}, e
	}
	b, e = os.ReadFile(filepath.Join(outDir, "result.json"))
	if e != nil {
		return Result{}, e
	}
	var r Result
	if e = json.Unmarshal(b, &r); e != nil {
		return Result{}, e
	}
	return r, nil
}
func putFile(ctx context.Context, e dockerx.Engine, container, target string, data []byte) error {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	if err := tw.WriteHeader(&tar.Header{Name: target[1:], Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
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
func unique() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "bench-verify-" + hex.EncodeToString(b)
}
