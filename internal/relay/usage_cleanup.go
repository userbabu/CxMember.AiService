package relay

import (
	"context"
	"log/slog"
	"time"
)

const usageRetention = 30 * 24 * time.Hour
const usageCleanupBatchSize = 500

// RunUsageCleanup performs an initial cleanup and then runs once every 24 hours.
func RunUsageCleanup(ctx context.Context, store *Store) {
	cleanup := func() {
		deleted, err := store.deleteExpiredUsage(ctx, time.Now().Add(-usageRetention))
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("[AIRelay] expired usage cleanup failed", "error", err)
			}
			return
		}
		slog.Info("[AIRelay] expired usage cleanup completed", "deleted_records", deleted)
	}

	cleanup()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func (s *Store) deleteExpiredUsage(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return total, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM ai_usage_pricing
			WHERE request_id IN (
				SELECT request_id FROM (
					SELECT request_id FROM ai_usage WHERE created_at < ? ORDER BY usage_id LIMIT ?
				) AS expired_usage
			)`, cutoff, usageCleanupBatchSize)
		if err == nil {
			res, deleteErr := tx.ExecContext(ctx, `DELETE FROM ai_usage
				WHERE usage_id IN (
					SELECT usage_id FROM (
						SELECT usage_id FROM ai_usage WHERE created_at < ? ORDER BY usage_id LIMIT ?
					) AS expired_usage
				)`, cutoff, usageCleanupBatchSize)
			err = deleteErr
			var result int64
			if err == nil {
				result, err = res.RowsAffected()
			}
			if err == nil {
				if err = tx.Commit(); err == nil {
					total += result
					if result < usageCleanupBatchSize {
						return total, nil
					}
					continue
				}
			}
		}
		_ = tx.Rollback()
		return total, err
	}
}
