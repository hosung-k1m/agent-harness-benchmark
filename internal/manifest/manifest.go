// Package manifest validates canonical variant configuration and gives it a stable digest.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

const (
	Model         = "gpt-5.6-luna"
	Reasoning     = "low"
	NetworkPolicy = "unrestricted-egress-v1"
)

type Variant struct {
	ID              string   `json:"id"`
	Image           string   `json:"image"`
	ImageDigest     string   `json:"image_digest"`
	Adapter         string   `json:"adapter"`
	Model           string   `json:"model"`
	ReasoningEffort string   `json:"reasoning_effort"`
	NetworkPolicyID string   `json:"network_policy_id"`
	ProtectedPaths  []string `json:"protected_paths"`
}

func (v Variant) Validate() error {
	for n, s := range map[string]string{"id": v.ID, "image": v.Image, "image_digest": v.ImageDigest, "adapter": v.Adapter} {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("manifest %s is required", n)
		}
	}
	if v.ID != "codex-cli" && v.ID != "dsh-default-codex" {
		return fmt.Errorf("unsupported variant id %q", v.ID)
	}
	if v.Model != Model || v.ReasoningEffort != Reasoning {
		return fmt.Errorf("model and reasoning must be pinned to %s/%s", Model, Reasoning)
	}
	if v.NetworkPolicyID != NetworkPolicy {
		return fmt.Errorf("unexpected network policy %q", v.NetworkPolicyID)
	}
	seen := make(map[string]struct{}, len(v.ProtectedPaths))
	for _, p := range v.ProtectedPaths {
		if !safePath(p) {
			return fmt.Errorf("unsafe protected path %q", p)
		}
		canonical := strings.TrimSuffix(p, "/")
		if _, exists := seen[canonical]; exists {
			return fmt.Errorf("duplicate protected path %q", p)
		}
		seen[canonical] = struct{}{}
	}
	return nil
}
func safePath(s string) bool {
	trimmed := strings.TrimSuffix(s, "/")
	return trimmed != "" && trimmed != "." && !strings.HasPrefix(s, "/") && !strings.HasPrefix(trimmed, "../") && !strings.Contains(s, "\\") && path.Clean(trimmed) == trimmed
}

// CanonicalJSON is deterministic, including protected-path ordering, without mutating v.
func CanonicalJSON(v Variant) ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	c := v
	c.ProtectedPaths = append([]string(nil), v.ProtectedPaths...)
	sort.Strings(c.ProtectedPaths)
	return json.Marshal(c)
}
func Hash(v Variant) (string, error) {
	b, e := CanonicalJSON(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
