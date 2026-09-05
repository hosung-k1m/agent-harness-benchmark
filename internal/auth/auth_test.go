package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDSHSeedsOnlyPinnedRecord(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	data := "version: 1\nrecords:\n  llm-pi-ai/openai-codex:\n    kind: grant\n    payload:\n      token: secret\n  other/provider:\n    kind: api\n    payload: no\n"
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := DSH{Source: p}.Material()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m[0].Target != "/home/bench/.dsh/.credentials.yaml" || strings.Contains(string(m[0].Data), "other/provider") {
		t.Fatalf("not minimal: %q", m[0].Data)
	}
	if len(m[0].Secrets) != 0 {
		t.Fatal("short non-secret YAML values should not become scan needles")
	}
}

func TestCodexAndDSHExtractSensitiveScalarNeedles(t *testing.T) {
	codexPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(codexPath, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"long-secret-access-token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	materials, err := (Codex{Source: codexPath}).Material()
	if err != nil || len(materials) != 1 || len(materials[0].Secrets) != 1 || string(materials[0].Secrets[0]) != "long-secret-access-token" {
		t.Fatalf("Codex secret scalar extraction failed: count=%d err=%v", len(materials[0].Secrets), err)
	}

	dshPath := filepath.Join(t.TempDir(), "credentials.yaml")
	data := "version: 1\nrecords:\n  llm-pi-ai/openai-codex:\n    kind: grant\n    payload:\n      access: long-secret-oauth-token\n"
	if err := os.WriteFile(dshPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	materials, err = (DSH{Source: dshPath}).Material()
	if err != nil || len(materials) != 1 || len(materials[0].Secrets) != 1 || string(materials[0].Secrets[0]) != "long-secret-oauth-token" {
		t.Fatalf("DSH secret scalar extraction failed: count=%d err=%v", len(materials[0].Secrets), err)
	}
}

func TestCodexRejectsAPIKeyOrNonSubscriptionAuth(t *testing.T) {
	for _, raw := range []string{
		`{"auth_mode":"api_key","OPENAI_API_KEY":"long-api-key-value"}`,
		`{"auth_mode":"chatgpt","OPENAI_API_KEY":"long-api-key-value"}`,
	} {
		p := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := (Codex{Source: p}).Material(); err == nil {
			t.Fatal("accepted non-subscription or API-key auth")
		}
	}
}

func TestDSHRejectsAlias(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(p, []byte("version: 1\nrecords:\n  llm-pi-ai/openai-codex: &x\n    kind: grant\n"), 0600)
	if _, err := (DSH{Source: p}).Material(); err == nil {
		t.Fatal("accepted YAML alias")
	}
}

func TestDSHRejectsNonOAuthRecordKind(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(p, []byte("version: 1\nrecords:\n  llm-pi-ai/openai-codex:\n    kind: api-key\n    key: long-api-key-value\n"), 0600)
	if _, err := (DSH{Source: p}).Material(); err == nil {
		t.Fatal("accepted non-OAuth DSH credential")
	}
}
