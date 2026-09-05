package artifact

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func makeTar(t *testing.T, name string, typ byte, body string) []byte {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if e := w.WriteHeader(&tar.Header{Name: name, Typeflag: typ, Size: int64(len(body))}); e != nil {
		t.Fatal(e)
	}
	if body != "" {
		w.Write([]byte(body))
	}
	w.Close()
	return b.Bytes()
}
func TestArchiveSafety(t *testing.T) {
	if ValidateTar(bytes.NewReader(makeTar(t, "../x", tar.TypeReg, "x")), DefaultLimits()) == nil {
		t.Fatal("traversal accepted")
	}
	if ValidateTar(bytes.NewReader(makeTar(t, "link", tar.TypeSymlink, "")), DefaultLimits()) == nil {
		t.Fatal("link accepted")
	}
	good := makeTar(t, "a.txt", tar.TypeReg, "ok")
	d := t.TempDir()
	if e := ExtractTar(bytes.NewReader(good), d, DefaultLimits()); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(d, "a.txt")); e != nil {
		t.Fatal(e)
	}
}

func TestTarStreamRootMarker(t *testing.T) {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteHeader(&tar.Header{Name: "./result.json", Typeflag: tar.TypeReg, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	if err := ExtractTar(bytes.NewReader(b.Bytes()), d, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "result.json")); err != nil {
		t.Fatal(err)
	}
}

func TestTarStreamRejectsDuplicateRootMarkers(t *testing.T) {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for range 2 {
		if err := w.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTar(bytes.NewReader(b.Bytes()), DefaultLimits()); err == nil {
		t.Fatal("duplicate root marker accepted")
	}
}

func TestArchiveRejectsDuplicateAndDirectoryEntryFloods(t *testing.T) {
	var duplicate bytes.Buffer
	w := tar.NewWriter(&duplicate)
	for range 2 {
		if err := w.WriteHeader(&tar.Header{Name: "same.txt", Typeflag: tar.TypeReg, Size: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if ValidateTar(bytes.NewReader(duplicate.Bytes()), DefaultLimits()) == nil {
		t.Fatal("duplicate archive member accepted")
	}

	var dirs bytes.Buffer
	w = tar.NewWriter(&dirs)
	for _, name := range []string{"a/", "b/", "c/"} {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if ValidateTar(bytes.NewReader(dirs.Bytes()), Limits{MaxFiles: 2, MaxFileBytes: 10, MaxTotalBytes: 10}) == nil {
		t.Fatal("directory entries bypassed entry limit")
	}
}
func TestProtectedAndSecrets(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(a, "README.md"), []byte("old"), 0600)
	os.WriteFile(filepath.Join(b, "README.md"), []byte("new"), 0600)
	changed, e := ProtectedChanged(a, b, []string{"README.md"})
	if e != nil || !changed {
		t.Fatal(e)
	}
	p := filepath.Join(b, "out")
	os.WriteFile(p, []byte("contains sensitive marker"), 0600)
	if RejectSecrets([]string{p}, [][]byte{[]byte("sensitive marker")}) == nil {
		t.Fatal("secret accepted")
	}
}

func TestProtectedDirectoryAddDeleteAndEmptyDirectoryChange(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(a, "tests", "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(b, "tests"), 0755); err != nil {
		t.Fatal(err)
	}
	changed, err := ProtectedChanged(a, b, []string{"tests/"})
	if err != nil || !changed {
		t.Fatalf("empty protected directory deletion was missed: changed=%v err=%v", changed, err)
	}
	if err := os.MkdirAll(filepath.Join(b, "tests", "added"), 0755); err != nil {
		t.Fatal(err)
	}
	changed, err = ProtectedChanged(a, b, []string{"tests/"})
	if err != nil || !changed {
		t.Fatalf("protected directory addition was missed: changed=%v err=%v", changed, err)
	}
}
