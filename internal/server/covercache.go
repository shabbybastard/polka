package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	coverCacheDirName = "cover-cache"
	// Covers not requested for this long are removed from disk. Reading a
	// cached cover refreshes its mtime, so the limit is about inactivity.
	coverCacheMaxAge = 14 * 24 * time.Hour
	coverCacheSweep  = 24 * time.Hour
)

// setupCoverCache enables the persistent cover cache and invalidates it when
// the catalog generation changed (for example after a CLI `import --replace`
// while the server was stopped). Book ids are remapped across a re-import, so
// serving covers from the previous generation would attach the wrong images.
func (s *Server) setupCoverCache(ctx context.Context) {
	if s.lib == nil || s.cfg.DataDir == "" {
		return
	}
	generation, err := s.st.CatalogGeneration(ctx)
	if err != nil {
		s.log.Warn("cover cache: catalog generation", "error", err)
		return
	}
	dir := filepath.Join(s.cfg.DataDir, coverCacheDirName)
	if err := prepareCoverCacheDir(dir, generation); err != nil {
		s.log.Warn("cover cache", "error", err)
		return
	}
	s.lib.SetCoverCacheDir(dir)
}

// resetCoverCache drops all extracted covers after a web import replaced the
// catalog and records the new generation.
func (s *Server) resetCoverCache(ctx context.Context) {
	if s.lib == nil || s.cfg.DataDir == "" {
		return
	}
	s.lib.ForgetAllCovers()
	generation, err := s.st.CatalogGeneration(ctx)
	if err != nil {
		s.log.Warn("cover cache: catalog generation", "error", err)
		return
	}
	dir := filepath.Join(s.cfg.DataDir, coverCacheDirName)
	if err := prepareCoverCacheDir(dir, generation); err != nil {
		s.log.Warn("cover cache", "error", err)
	}
}

// coverCacheLoop prunes old covers at startup and then once a day.
func (s *Server) coverCacheLoop(ctx context.Context) {
	s.cleanupCoverCache()
	ticker := time.NewTicker(coverCacheSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupCoverCache()
		}
	}
}

// cleanupCoverCache removes cached covers (and negative markers) that were
// not requested for coverCacheMaxAge. Disk hits refresh mtime, so actively
// used covers survive.
func (s *Server) cleanupCoverCache() {
	if s.cfg.DataDir == "" {
		return
	}
	root := filepath.Join(s.cfg.DataDir, coverCacheDirName)
	shards, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-coverCacheMaxAge)
	removed := 0
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		dir := filepath.Join(root, shard.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		left := 0
		for _, file := range files {
			info, err := file.Info()
			if err != nil {
				left++
				continue
			}
			if info.IsDir() || info.ModTime().After(cutoff) {
				left++
				continue
			}
			if err := os.Remove(filepath.Join(dir, file.Name())); err != nil {
				left++
				continue
			}
			removed++
		}
		if left == 0 {
			os.Remove(dir) // only succeeds when empty
		}
	}
	if removed > 0 {
		s.log.Info("cover cache cleanup", "removed", removed, "max_age", coverCacheMaxAge.String())
	}
}

// prepareCoverCacheDir keeps the cache only when the stored generation
// matches. The generation file lives next to the directory so clearing the
// cache itself does not lose the marker.
func prepareCoverCacheDir(dir string, generation int64) error {
	genPath := dir + ".generation"
	want := strconv.FormatInt(generation, 10)
	if b, err := os.ReadFile(genPath); err == nil && string(b) == want {
		return os.MkdirAll(dir, 0o755)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(genPath, []byte(want), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", genPath, err)
	}
	return nil
}
