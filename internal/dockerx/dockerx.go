// Package dockerx is the deliberately small Docker Engine boundary used by bench.
package dockerx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Engine is intentionally command-shaped so tests can use a fake without Docker.
type Engine interface {
	Run(context.Context, ...string) ([]byte, error)
}

// InputEngine is required for transfers into read-only containers. Docker cp
// cannot write through a read-only rootfs even when the destination is tmpfs.
type InputEngine interface {
	Engine
	RunInput(context.Context, []byte, ...string) ([]byte, error)
}

type CLI struct{ Binary string }

func (c CLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	b := c.Binary
	if b == "" {
		b = "docker"
	}
	cmd := exec.CommandContext(ctx, b, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
func (c CLI) RunInput(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	b := c.Binary
	if b == "" {
		b = "docker"
	}
	cmd := exec.CommandContext(ctx, b, args...)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func NetworkCreate(ctx context.Context, e Engine, name string) error {
	_, err := e.Run(ctx, "network", "create", "--internal=false", name)
	return err
}
func NetworkRemove(ctx context.Context, e Engine, name string) error {
	_, err := e.Run(ctx, "network", "rm", name)
	return err
}
func RemoveContainer(ctx context.Context, e Engine, name string) error {
	_, err := e.Run(ctx, "rm", "-f", name)
	return err
}
func Start(ctx context.Context, e Engine, name string) error {
	_, err := e.Run(ctx, "start", name)
	return err
}
func CopyTo(ctx context.Context, e Engine, src, container, dest string) error {
	_, err := e.Run(ctx, "cp", src, container+":"+dest)
	return err
}
func CopyFrom(ctx context.Context, e Engine, container, src, dest string) error {
	_, err := e.Run(ctx, "cp", container+":"+src, dest)
	return err
}

// ExtractTar streams a tar archive into an existing writable tmpfs destination.
func ExtractTar(ctx context.Context, e Engine, container, dest string, archive []byte) error {
	ie, ok := e.(InputEngine)
	if !ok {
		return fmt.Errorf("docker engine does not support input streaming")
	}
	_, err := ie.RunInput(ctx, archive, "exec", "-i", "--user", "10001", container, "tar", "-xf", "-", "-C", dest)
	return err
}

// CopyOut reads a tar stream through docker exec because the Docker daemon's
// docker cp implementation cannot see files in a container tmpfs.
func CopyOut(ctx context.Context, e Engine, container, src string) ([]byte, error) {
	return e.Run(ctx, "exec", "--user", "10001", container, "tar", "-cf", "-", "-C", src, ".")
}
func Exec(ctx context.Context, e Engine, name string, args ...string) ([]byte, error) {
	return e.Run(ctx, append([]string{"exec", "--user", "10001", name}, args...)...)
}

// CreateInert starts no agent process. Writable paths are tmpfs; root and caps stay locked down.
func CreateInert(ctx context.Context, e Engine, name, network, image string, cpus float64, memory, tmpfs int64, pids, uid int) error {
	user := fmt.Sprint(uid)
	tmp := "rw,nosuid,nodev,uid=" + user + ",gid=" + user + ",mode=0700,size=" + fmt.Sprint(tmpfs)
	// Codex workspace-write uses an unprivileged bwrap namespace; Docker's
	// default seccomp profile blocks it. No capabilities or privileges are added.
	args := []string{"create", "--name", name, "--network", network, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--security-opt", "seccomp=unconfined", "--pids-limit", fmt.Sprint(pids), "--memory", fmt.Sprint(memory), "--memory-swap", fmt.Sprint(memory), "--cpus", fmt.Sprintf("%.2f", cpus), "--user", user, "--tmpfs", "/workspace:" + tmp, "--tmpfs", "/home/bench:" + tmp, "--tmpfs", "/tmp:" + tmp, image, "sleep", "infinity"}
	_, err := e.Run(ctx, args...)
	return err
}
