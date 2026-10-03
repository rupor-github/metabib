package db

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// EnsureReadIndexes adds indexes missing from imported dumps before batch reads begin.
func (r *Repository) EnsureReadIndexes(ctx context.Context, log *zap.Logger) error {
	if r.currentFormat() != FormatFlibustaCurrent {
		return nil
	}
	exists, err := r.tableExists(ctx, "libbannotations")
	if err != nil || !exists {
		return err
	}
	// A full leading BookId key is enough to avoid scanning the annotation table
	// for every batch. Reuse it even if its name or trailing columns differ.
	var count int
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.statistics
 WHERE table_schema = DATABASE() AND table_name = 'libbannotations'
   AND column_name = 'BookId' AND seq_in_index = 1
   AND sub_part IS NULL AND index_type = 'BTREE'`).Scan(&count); err != nil {
		return fmt.Errorf("check annotation read indexes: %w", err)
	}
	if count > 0 {
		return nil
	}
	start := time.Now()
	if log != nil {
		log.Info("Creating database read index", zap.String("table", "libbannotations"), zap.String("index", "metabib_bookid_nid"))
	}
	if _, err := r.db.ExecContext(ctx, "CREATE INDEX metabib_bookid_nid ON libbannotations (BookId, nid)"); err != nil {
		return fmt.Errorf("create annotation read index: %w", err)
	}
	if log != nil {
		log.Info(
			"Database read index created",
			zap.String("table", "libbannotations"),
			zap.String("index", "metabib_bookid_nid"),
			zap.Duration("elapsed", time.Since(start)),
		)
	}
	return nil
}
