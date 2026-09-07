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

// SupportedModels is the complete model/reasoning contract exposed by the UI.
// Keep this here so manifests, APIs, and runners validate the same matrix.
var SupportedModels = map[string][]string{
	"gpt-6-astra":   {"low", "medium", "high", "xhigh", "max", "ultra"},
	"gpt-5.6-sol":   {"low", "medium", "high", "xhigh", "max", "ultra"},
	"gpt-5.6-terra": {"low", "medium", "high", "xhigh", "max", "ultra"},
	"gpt-5.6-luna":  {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.5":       {"low", "medium", "high", "xhigh"},
	"gpt-5.4-mini":  {"low", "medium", "high", "xhigh"},
}

func ValidModelReasoning(model, reasoning string) bool {
	for _, allowed := range SupportedModels[model] {
		if reasoning == allowed {
			return true
		}
	}
	return false
}

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
	if v.ID != "codex-cli" && v.ID != "dsh-default-codex" && v.ID != "dsh-modified-codex" {
		return fmt.Errorf("unsupported variant id %q", v.ID)
	}
	if !ValidModelReasoning(v.Model, v.ReasoningEffort) {
		return fmt.Errorf("unsupported model/reasoning pair %s/%s", v.Model, v.ReasoningEffort)
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
