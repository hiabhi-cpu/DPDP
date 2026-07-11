package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyWriterCreatesDatedFile(t *testing.T) {
	dir := t.TempDir()
	w := &dailyLumberjackWriter{baseDir: dir, baseName: "app.log"}

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, today, "app.log")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected log file at %s: %v", path, err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("got %q, want %q", b, "hello\n")
	}
}

func TestRotateAtStartupRenamesExisting(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format("2006-01-02")
	dayDir := filepath.Join(dir, today)
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(dayDir, "app.log")
	if err := os.WriteFile(cur, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := rotateAtStartup(dir, "app.log"); err != nil {
		t.Fatalf("rotateAtStartup: %v", err)
	}

	if _, err := os.Stat(cur); !os.IsNotExist(err) {
		t.Fatalf("expected app.log renamed away, stat err=%v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dayDir, "app-restarted-*.log"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 restarted backup, got %d", len(matches))
	}
}

func TestRotateAtStartupNoFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := rotateAtStartup(dir, "app.log"); err != nil {
		t.Fatalf("expected nil when file absent, got %v", err)
	}
}
