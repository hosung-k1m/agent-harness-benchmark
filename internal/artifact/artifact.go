// Package artifact handles the narrow, safe boundary between an agent workspace and verifier.
package artifact

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Limits struct {
	MaxFiles                    int
	MaxFileBytes, MaxTotalBytes int64
}

func DefaultLimits() Limits {
	return Limits{MaxFiles: 10000, MaxFileBytes: 16 << 20, MaxTotalBytes: 128 << 20}
}
func (l Limits) check() Limits {
	if l.MaxFiles <= 0 || l.MaxFileBytes <= 0 || l.MaxTotalBytes <= 0 {
		return DefaultLimits()
	}
	return l
}
func safeName(n string) bool {
	n = strings.TrimPrefix(n, "./")
	n = strings.TrimSuffix(n, "/")
	return n != "" && n != "." && !strings.HasPrefix(n, "/") && !strings.Contains(n, "\\") && !strings.Contains("/"+n+"/", "/../") && !strings.HasPrefix(n, "../")
}
func archiveName(n string) string {
	if n == "." || n == "./" {
		return "."
	}
	return strings.TrimSuffix(strings.TrimPrefix(n, "./"), "/")
}

// ValidateTar consumes and validates every entry. Symlinks are rejected entirely: this is
// stricter than merely checking escape and removes a whole class of verifier extraction bugs.
func ValidateTar(r io.Reader, l Limits) error {
	l = l.check()
	tr := tar.NewReader(r)
	entries := 0
	seen := make(map[string]struct{})
	rootSeen := false
	var total int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return fmt.Errorf("read tar: %w", e)
		}
		name := archiveName(h.Name)
		// GNU tar's normal `-C source .` stream starts with one root directory
		// marker. It has no extraction effect and is allowed only as a directory.
		if name == "." {
			if h.Typeflag != tar.TypeDir {
				return fmt.Errorf("unsafe archive path")
			}
			if rootSeen {
				return fmt.Errorf("archive contains duplicate entry")
			}
			rootSeen = true
			entries++
			if entries > l.MaxFiles {
				return fmt.Errorf("archive exceeds entry limit")
			}
			continue
		}
		if !safeName(h.Name) {
			return fmt.Errorf("unsafe archive path")
		}
		// A duplicate member can overwrite an earlier protected file when a
		// consumer extracts without O_EXCL. Reject it before any extraction.
		if _, ok := seen[name]; ok {
			return fmt.Errorf("archive contains duplicate entry")
		}
		seen[name] = struct{}{}
		entries++
		if entries > l.MaxFiles {
			return fmt.Errorf("archive exceeds entry limit")
		}
		if h.Size < 0 || h.Size > l.MaxFileBytes {
			return fmt.Errorf("archive file size exceeds limit")
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir:
		default:
			return fmt.Errorf("archive contains forbidden entry type")
		}
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA {
			total += h.Size
			if total > l.MaxTotalBytes {
				return fmt.Errorf("archive exceeds limits")
			}
			if _, e = io.Copy(io.Discard, tr); e != nil {
				return e
			}
		}
	}
}

func ExtractTar(r io.Reader, dest string, l Limits) error {
	var buf bytes.Buffer
	if _, e := io.Copy(&buf, r); e != nil {
		return e
	}
	if e := ValidateTar(bytes.NewReader(buf.Bytes()), l); e != nil {
		return e
	}
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	root, e := filepath.Abs(dest)
	if e != nil {
		return e
	}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		if archiveName(h.Name) == "." {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(h.Name))
		rel, e := filepath.Rel(root, target)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive path escapes destination")
		}
		if h.Typeflag == tar.TypeDir {
			if e = os.MkdirAll(target, 0755); e != nil {
				return e
			}
			continue
		}
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			return e
		}
		f, e := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0644)
		if e != nil {
			return e
		}
		_, copyErr := io.Copy(f, tr)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

// ArchiveDir creates a verifier-safe regular-files-only tar. It never follows symlinks.
func ArchiveDir(src string, w io.Writer, l Limits) error {
	l = l.check()
	root, e := filepath.Abs(src)
	if e != nil {
		return e
	}
	tw := tar.NewWriter(w)
	entries := 0
	var total int64
	e = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil || !safeName(filepath.ToSlash(rel)) {
			return fmt.Errorf("unsafe workspace path")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace contains symlink")
		}
		if d.IsDir() {
			entries++
			if entries > l.MaxFiles {
				return fmt.Errorf("workspace exceeds entry limit")
			}
			return tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel) + "/", Mode: 0755, Typeflag: tar.TypeDir})
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("workspace contains non-regular file")
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Size() > l.MaxFileBytes {
			return fmt.Errorf("workspace file exceeds limit")
		}
		entries++
		total += info.Size()
		if entries > l.MaxFiles || total > l.MaxTotalBytes {
			return fmt.Errorf("workspace exceeds limits")
		}
		if e = tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0644, Size: info.Size(), Typeflag: tar.TypeReg}); e != nil {
			return e
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if e != nil {
		return e
	}
	return tw.Close()
}

func contains(data []byte, secrets [][]byte) bool {
	for _, s := range secrets {
		if len(s) > 0 && bytes.Contains(data, s) {
			return true
		}
	}
	return false
}

// RejectSecrets detects exact in-memory seeded values without ever returning or formatting them.
func RejectSecrets(paths []string, secrets [][]byte) error {
	for _, p := range paths {
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		if contains(b, secrets) {
			return fmt.Errorf("credential leakage detected in collected artifact")
		}
	}
	return nil
}

func digestFile(path string) ([32]byte, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return [32]byte{}, e
	}
	return sha256.Sum256(b), nil
}

// ProtectedChanged compares every protected path in two workspace roots. Missing paths count as a change.
func ProtectedChanged(before, after string, protected []string) (bool, error) {
	for _, p := range protected {
		if !safeName(p) {
			return false, fmt.Errorf("unsafe protected path")
		}
		a, e := treeDigest(filepath.Join(before, p))
		if e != nil {
			return false, e
		}
		b, e := treeDigest(filepath.Join(after, p))
		if e != nil {
			return false, e
		}
		if a != b {
			return true, nil
		}
	}
	return false, nil
}
func treeDigest(path string) ([32]byte, error) {
	var zero [32]byte
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return zero, nil
	}
	if e != nil {
		return zero, e
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return zero, fmt.Errorf("symlink in protected path")
	}
	h := sha256.New()
	// Include every entry and its type. This catches protected-directory
	// additions/deletions even when they are empty, and file-vs-dir changes.
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return zero, fmt.Errorf("non-regular file in protected path")
		}
		io.WriteString(h, "file\\x00")
		d, e := digestFile(path)
		if e != nil {
			return zero, e
		}
		h.Write(d[:])
		copy(zero[:], h.Sum(nil))
		return zero, nil
	}
	var paths []string
	e = filepath.WalkDir(path, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in protected path")
		}
		paths = append(paths, p)
		return nil
	})
	if e != nil {
		return zero, e
	}
	sort.Strings(paths)
	for _, p := range paths {
		rel, _ := filepath.Rel(path, p)
		info, e := os.Lstat(p)
		if e != nil {
			return zero, e
		}
		if info.IsDir() {
			io.WriteString(h, "dir\\x00"+filepath.ToSlash(rel)+"\\x00")
			continue
		}
		if !info.Mode().IsRegular() {
			return zero, fmt.Errorf("non-regular file in protected path")
		}
		d, e := digestFile(p)
		if e != nil {
			return zero, e
		}
		io.WriteString(h, "file\\x00"+filepath.ToSlash(rel)+"\\x00")
		h.Write(d[:])
	}
	copy(zero[:], h.Sum(nil))
	return zero, nil
}
