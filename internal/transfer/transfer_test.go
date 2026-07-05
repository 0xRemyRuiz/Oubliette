package transfer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDump_copiesFiles(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "core-1.img"), []byte("checkpoint data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "nested.img"), []byte("nested"), 0600); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "shared", "oubliette-dump")
	if err := CopyDump(src, dest); err != nil {
		t.Fatalf("CopyDump: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "core-1.img"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(got) != "checkpoint data" {
		t.Errorf("core-1.img: got %q, want %q", got, "checkpoint data")
	}

	gotNested, err := os.ReadFile(filepath.Join(dest, "sub", "nested.img"))
	if err != nil {
		t.Fatalf("read copied nested file: %v", err)
	}
	if string(gotNested) != "nested" {
		t.Errorf("sub/nested.img: got %q, want %q", gotNested, "nested")
	}
}

func TestCopyDump_missingSource(t *testing.T) {
	dest := t.TempDir()
	err := CopyDump("/nonexistent/dump/dir", dest)
	if err == nil {
		t.Fatal("expected error for missing source dir, got nil")
	}
}

func TestCopyDump_createsDestTree(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := CopyDump(src, dest); err != nil {
		t.Fatalf("CopyDump: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "f")); err != nil {
		t.Errorf("expected destination tree to be created: %v", err)
	}
}

func TestStageHelper_copiesNewBinary(t *testing.T) {
	local := filepath.Join(t.TempDir(), "oubliette-restorehelper")
	if err := os.WriteFile(local, []byte("binary contents v1"), 0755); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(t.TempDir(), "shared")

	if err := StageHelper(local, destDir); err != nil {
		t.Fatalf("StageHelper: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "oubliette-restorehelper"))
	if err != nil {
		t.Fatalf("read staged helper: %v", err)
	}
	if string(got) != "binary contents v1" {
		t.Errorf("staged helper: got %q, want %q", got, "binary contents v1")
	}
}

func TestStageHelper_skipsIdenticalBinary(t *testing.T) {
	local := filepath.Join(t.TempDir(), "oubliette-restorehelper")
	if err := os.WriteFile(local, []byte("binary contents v1"), 0755); err != nil {
		t.Fatal(err)
	}
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "oubliette-restorehelper")
	if err := os.WriteFile(destPath, []byte("binary contents v1"), 0755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(destPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := StageHelper(local, destDir); err != nil {
		t.Fatalf("StageHelper: %v", err)
	}

	after, err := os.Stat(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("expected identical staged binary to be left untouched (mtime changed), but StageHelper recopied it")
	}
}

func TestStageHelper_recopiesChangedBinary(t *testing.T) {
	local := filepath.Join(t.TempDir(), "oubliette-restorehelper")
	if err := os.WriteFile(local, []byte("binary contents v2"), 0755); err != nil {
		t.Fatal(err)
	}
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "oubliette-restorehelper")
	if err := os.WriteFile(destPath, []byte("stale contents"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := StageHelper(local, destDir); err != nil {
		t.Fatalf("StageHelper: %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary contents v2" {
		t.Errorf("staged helper: got %q, want %q", got, "binary contents v2")
	}
}

func TestStageHelper_missingSource(t *testing.T) {
	err := StageHelper("/nonexistent/oubliette-restorehelper", t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing source binary, got nil")
	}
}
