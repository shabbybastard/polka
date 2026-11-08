// Package importer connects catalog sources (inpx, folder scanning)
// to the storage layer.
package importer

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/shabbybastard/polka/internal/inpx"
	"github.com/shabbybastard/polka/internal/store"
)

// Progress reports import progress: phase is "loading" or "indexing".
type Progress func(phase string, processed int)

// ImportInpx loads an inpx catalog into an empty database.
func ImportInpx(ctx context.Context, log *slog.Logger, st *store.Store, path string, onProgress Progress) (store.ImportStats, error) {
	var stats store.ImportStats

	f, err := inpx.Open(path)
	if err != nil {
		return stats, err
	}
	defer f.Close()

	if name := f.CollectionName(); name != "" {
		if err := st.SetMeta(ctx, "collection_name", name); err != nil {
			return stats, err
		}
		log.Info("collection", "name", name)
	}

	session, err := st.NewImport(ctx)
	if err != nil {
		return stats, err
	}
	defer func() {
		if err := session.Abort(); err != nil {
			log.Warn("import cleanup", "error", err)
		}
	}()

	processed := 0
	err = f.Records(func(rec *inpx.Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := session.Add(toBookInput(rec)); err != nil {
			return err
		}
		if processed++; processed%50_000 == 0 {
			log.Info("importing", "books", processed)
		}
		if onProgress != nil && processed%5_000 == 0 {
			onProgress("loading", processed)
		}
		return nil
	})
	if err != nil {
		return stats, err
	}

	log.Info("building indexes and search index", "books", processed)
	if onProgress != nil {
		onProgress("indexing", processed)
	}
	return session.Finish()
}

// ReplaceHooks lets the caller remap external user data after the catalog
// has been swapped. AfterReplace receives the old and new id->key maps.
type ReplaceHooks struct {
	// BeforeReplace runs after staging import but before the live catalog is
	// touched. It can persist the future remap for crash recovery.
	BeforeReplace func(oldKeys, newKeys map[int64]string) error
	AfterReplace  func(oldKeys, newKeys map[int64]string) error
}

// ReplaceInpx imports an inpx into a fresh staging database and only then
// atomically swaps it into target. A malformed file, a crash during the
// import or an abort therefore leaves the live catalog untouched.
func ReplaceInpx(ctx context.Context, log *slog.Logger, target *store.Store, inpxPath, dataDir string, onProgress Progress, hooks ReplaceHooks) (store.ImportStats, error) {
	var stats store.ImportStats
	oldKeys, err := target.BookKeys(ctx)
	if err != nil {
		return stats, err
	}
	if dataDir == "" {
		dataDir = os.TempDir()
	}
	tmp, err := os.CreateTemp(dataDir, "polka-import-*.db")
	if err != nil {
		return stats, fmt.Errorf("create staging database: %w", err)
	}
	stagingPath := tmp.Name()
	tmp.Close()
	cleanup := func() {
		os.Remove(stagingPath)
		os.Remove(stagingPath + "-wal")
		os.Remove(stagingPath + "-shm")
	}
	defer cleanup()

	staging, err := store.Open(stagingPath)
	if err != nil {
		return stats, fmt.Errorf("open staging database: %w", err)
	}
	stats, err = ImportInpx(ctx, log, staging, inpxPath, onProgress)
	var newKeys map[int64]string
	if err == nil {
		newKeys, err = staging.BookKeys(ctx)
	}
	closeErr := staging.Close()
	if err != nil {
		return stats, err
	}
	if closeErr != nil {
		return stats, fmt.Errorf("close staging database: %w", closeErr)
	}
	if hooks.BeforeReplace != nil {
		if err := hooks.BeforeReplace(oldKeys, newKeys); err != nil {
			return stats, err
		}
	}
	if err := target.ReplaceFrom(ctx, stagingPath); err != nil {
		return stats, err
	}
	if hooks.AfterReplace != nil {
		if err := hooks.AfterReplace(oldKeys, newKeys); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func toBookInput(rec *inpx.Record) *store.BookInput {
	b := &store.BookInput{
		LibID:     rec.LibID,
		Title:     rec.Title,
		Series:    rec.Series,
		SeriesNum: rec.SeriesNum,
		Genres:    rec.Genres,
		Keywords:  rec.Keywords,
		Folder:    rec.Folder,
		File:      rec.File,
		Ext:       rec.Ext,
		Size:      rec.Size,
		Lang:      rec.Lang,
		Year:      rec.Year,
		Added:     rec.Date,
		Rate:      rec.Rate,
		Deleted:   rec.Deleted,
	}
	for _, a := range rec.Authors {
		b.Authors = append(b.Authors, store.AuthorName{Last: a.Last, First: a.First, Middle: a.Middle})
	}
	return b
}
