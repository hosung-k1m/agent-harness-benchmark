// Package state stores immutable successful preflight and smoke records.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Record struct {
	Kind       string    `json:"kind"`
	VariantID  string    `json:"variant_id"`
	ConfigHash string    `json:"config_hash"`
	CreatedAt  time.Time `json:"created_at"`
	Success    bool      `json:"success"`
}

func (r Record) Validate() error {
	if !validKind(r.Kind) || !validVariant(r.VariantID) || !validHash(r.ConfigHash) || r.CreatedAt.IsZero() || !r.Success {
		return fmt.Errorf("invalid state record")
	}
	return nil
}
func Hash(r Record) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	b, e := json.Marshal(r)
	if e != nil {
		return "", e
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}
func Path(root string, r Record) string {
	return filepath.Join(root, r.Kind, r.VariantID, r.ConfigHash+".json")
}
func Write(root string, r Record) error {
	if err := r.Validate(); err != nil {
		return err
	}
	p := Path(root, r)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	} // O_EXCL makes state append-only.
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(e) {
		return fmt.Errorf("immutable state already exists: %s", p)
	}
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = f.Write(append(b, '\n'))
	return e
}
func Read(root, kind, variant, configHash string) (Record, error) {
	if !validKind(kind) || !validVariant(variant) || !validHash(configHash) {
		return Record{}, fmt.Errorf("invalid state identity")
	}
	p := filepath.Join(root, kind, variant, configHash+".json")
	b, e := os.ReadFile(p)
	if e != nil {
		return Record{}, e
	}
	var r Record
	if e = json.Unmarshal(b, &r); e != nil {
		return Record{}, e
	}
	if e = r.Validate(); e != nil {
		return Record{}, e
	}
	if r.Kind != kind || r.VariantID != variant || r.ConfigHash != configHash {
		return Record{}, fmt.Errorf("state identity mismatch")
	}
	return r, nil
}

func validKind(kind string) bool { return kind == "preflight" || kind == "smoke" }

// Variant ids are deliberately filename-safe; state callers never get to
// smuggle traversal components into a state path.
func validVariant(id string) bool {
	return id == "codex-cli" || id == "dsh-default-codex"
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func Current(root, kind, variant, configHash string) bool {
	_, e := Read(root, kind, variant, configHash)
	return e == nil
}
