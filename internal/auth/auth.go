// Package auth provides narrow credential reads and never prints secret contents.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Material struct {
	Target  string
	Data    []byte
	Secrets [][]byte
}
type Provider interface{ Material() ([]Material, error) }

// Codex reads precisely auth.json. It deliberately does not copy a whole HOME/config directory.
type Codex struct{ Source string }

func (c Codex) Material() ([]Material, error) {
	p := c.Source
	if p == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		p = filepath.Join(h, ".codex", "auth.json")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("Codex login unavailable; run codex login: %w", err)
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		return nil, fmt.Errorf("Codex auth.json is invalid")
	}
	if mode, ok := v["auth_mode"].(string); !ok || mode != "chatgpt" {
		return nil, fmt.Errorf("Codex auth is not a ChatGPT subscription login")
	}
	if key, present := v["OPENAI_API_KEY"]; present && key != nil && key != "" {
		return nil, fmt.Errorf("Codex auth contains an API key; subscription login is required")
	}
	return []Material{{Target: "/home/bench/.codex/auth.json", Data: b, Secrets: jsonSecrets(v)}}, nil
}

// DSH never derives credentials from Codex. The record must be the supported provider identity.
type DSH struct{ Source string }

func (d DSH) Material() ([]Material, error) {
	if d.Source == "" {
		h, e := os.UserHomeDir()
		if e != nil {
			return nil, e
		}
		d.Source = filepath.Join(h, ".dsh", ".credentials.yaml")
	}
	b, err := os.ReadFile(d.Source)
	if err != nil {
		return nil, fmt.Errorf("DSH openai-codex OAuth is unavailable; run the supported DSH login for llm-pi-ai/openai-codex")
	}
	seed, ok := dshRecord(b)
	if !ok {
		return nil, fmt.Errorf("DSH openai-codex OAuth is unavailable; run the supported DSH login for llm-pi-ai/openai-codex")
	}
	return []Material{{Target: "/home/bench/.dsh/.credentials.yaml", Data: seed, Secrets: yamlSecrets(seed)}}, nil
}

// DSHCodex supplies the DSH parent OAuth record and Codex plugin credentials.
// The sources are optional independently only for tests; production defaults
// resolve both documented login locations.
type DSHCodex struct{ DSHSource, CodexSource string }

func (d DSHCodex) Material() ([]Material, error) {
	dsh, err := (DSH{Source: d.DSHSource}).Material()
	if err != nil {
		return nil, err
	}
	codex, err := (Codex{Source: d.CodexSource}).Material()
	if err != nil {
		return nil, err
	}
	return append(dsh, codex...), nil
}

func jsonSecrets(v any) [][]byte {
	var out [][]byte
	var walk func(any)
	walk = func(value any) {
		switch x := value.(type) {
		case map[string]any:
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		case string:
			// Short enum-like strings are not sensitive and create noisy false
			// positives. OAuth/account values are substantially longer.
			if len(x) >= 12 {
				out = append(out, []byte(x))
			}
		}
	}
	walk(v)
	return out
}

func yamlSecrets(b []byte) [][]byte {
	var out [][]byte
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, ":") {
			continue
		}
		value := strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[1])
		value = strings.Trim(value, "'\"")
		if len(value) >= 12 {
			out = append(out, []byte(value))
		}
	}
	return out
}

// dshRecord accepts the documented simple YAML layout only, rejects YAML aliases,
// and emits a new document containing solely the pinned provider record.
func dshRecord(b []byte) ([]byte, bool) {
	s := string(b)
	if strings.ContainsAny(s, "&*") {
		return nil, false
	}
	lines := strings.Split(s, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "llm-pi-ai/openai-codex:" && len(l)-len(strings.TrimLeft(l, " ")) == 2 {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) != "" && len(l)-len(strings.TrimLeft(l, " ")) <= 2 {
			end = i
			break
		}
	}
	if end <= start+1 {
		return nil, false
	}
	grant := false
	for _, l := range lines[start+1 : end] {
		if len(l)-len(strings.TrimLeft(l, " ")) == 4 && strings.TrimSpace(l) == "kind: grant" {
			grant = true
			break
		}
	}
	if !grant {
		return nil, false
	}
	var out strings.Builder
	out.WriteString("version: 1\nrecords:\n")
	for _, l := range lines[start:end] {
		out.WriteString(l)
		out.WriteByte('\n')
	}
	return []byte(out.String()), true
}
