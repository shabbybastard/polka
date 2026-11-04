package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vestigiumincaligne/polka/internal/config"
)

func TestCleanupBackups(t *testing.T) {
	dir := t.TempDir()
	oldDir := filepath.Join(dir, "epub-backups", "1")
	newDir := filepath.Join(dir, "epub-backups", "2")
	for _, d := range []string{oldDir, newDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldFile := filepath.Join(oldDir, "old.epub")
	newFile := filepath.Join(newDir, "new.epub")
	os.WriteFile(oldFile, []byte("old"), 0o644)
	os.WriteFile(newFile, []byte("new"), 0o644)
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(oldFile, oldTime, oldTime)

	s := &Server{cfg: &config.Config{DataDir: dir}}
	s.cleanupBackups()

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old backup was not removed")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Error("fresh backup was removed")
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("empty backup directory was not removed")
	}
}
