package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	backupDirName = "epub-backups"
	backupMaxAge  = 7 * 24 * time.Hour
	backupHour    = 3 // local server time
)

func (s *Server) backupEPUB(bookID int64, data []byte) error {
	dir := filepath.Join(s.cfg.DataDir, backupDirName, strconv.FormatInt(bookID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := time.Now().UTC().Format("20060102-150405.000000") + ".epub"
	tmp, err := os.CreateTemp(dir, ".backup-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}

// backupLoop removes backups older than a week once a day at 03:00 local
// time. It also runs immediately on startup so a server that was offline
// for a while catches up without waiting for the next night.
func (s *Server) backupLoop(ctx context.Context) {
	s.cleanupBackups()
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), backupHour, 0, 0, 0, now.Location())
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.cleanupBackups()
		}
	}
}

func (s *Server) cleanupBackups() {
	root := filepath.Join(s.cfg.DataDir, backupDirName)
	cutoff := time.Now().Add(-backupMaxAge)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, dir := range entries {
		if !dir.IsDir() {
			continue
		}
		bookDir := filepath.Join(root, dir.Name())
		files, err := os.ReadDir(bookDir)
		if err != nil {
			continue
		}
		left := 0
		for _, file := range files {
			if file.IsDir() {
				continue
			}
			info, err := file.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(bookDir, file.Name()))
			} else {
				left++
			}
		}
		if left == 0 {
			os.Remove(bookDir)
		}
	}
}
