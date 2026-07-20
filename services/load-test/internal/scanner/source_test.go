package scanner

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeZip builds a .zip staging file for token under root and returns its path.
func writeZip(t *testing.T, root, token string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(filepath.Join(root, token+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireUploadExtractsAndCleansUp(t *testing.T) {
	work := t.TempDir()
	staging := t.TempDir()
	writeZip(t, staging, "tok123", map[string]string{
		"main.go":     "package main\nfunc main(){}\n",
		"pkg/util.go": "package pkg\n",
	})

	dir, cleanup, err := Acquire(context.Background(), work, staging, Source{Type: "upload", Token: "tok123"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Errorf("expected extracted main.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg", "util.go")); err != nil {
		t.Errorf("expected extracted pkg/util.go: %v", err)
	}
	// staging must be consumed after extraction ([EPHEM-01])
	if _, err := os.Stat(filepath.Join(staging, "tok123.zip")); !os.IsNotExist(err) {
		t.Errorf("staging archive should be removed after extract")
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove work dir, got err=%v", err)
	}
}

func TestAcquireUploadMissingStaging(t *testing.T) {
	work := t.TempDir()
	staging := t.TempDir()
	_, _, err := Acquire(context.Background(), work, staging, Source{Type: "upload", Token: "nope"})
	if err == nil {
		t.Fatal("expected error for missing staging")
	}
	// work dir must be cleaned up on failure (no orphan)
	entries, _ := os.ReadDir(work)
	if len(entries) != 0 {
		t.Errorf("failed Acquire must leave no orphan work dir, got %d entries", len(entries))
	}
}

func TestExtractZipSlipRejected(t *testing.T) {
	work := t.TempDir()
	staging := t.TempDir()
	// craft a zip with a traversal entry
	writeZip(t, staging, "evil", map[string]string{
		"../../escape.txt": "pwned",
	})
	_, _, err := Acquire(context.Background(), work, staging, Source{Type: "upload", Token: "evil"})
	if err == nil {
		t.Fatal("expected zip-slip rejection")
	}
}

func TestAcquireUnknownType(t *testing.T) {
	work := t.TempDir()
	_, _, err := Acquire(context.Background(), work, work, Source{Type: "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown source type")
	}
}
