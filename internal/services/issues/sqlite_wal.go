package issues

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

var (
	sqliteWALMaintenanceInterval = time.Minute
	sqliteWALCheckpointThreshold = int64(64 * 1024 * 1024)
	sqliteWALLargeThreshold      = int64(512 * 1024 * 1024)
)

type SQLiteWALCheckpointMode string

const (
	SQLiteWALCheckpointPassive  SQLiteWALCheckpointMode = "PASSIVE"
	SQLiteWALCheckpointTruncate SQLiteWALCheckpointMode = "TRUNCATE"
)

type SQLiteWALDiagnostics struct {
	DBPath              string                    `json:"db_path"`
	WALPath             string                    `json:"wal_path"`
	WALBytes            int64                     `json:"wal_bytes"`
	CheckpointThreshold int64                     `json:"checkpoint_threshold_bytes"`
	LargeThreshold      int64                     `json:"large_threshold_bytes"`
	Large               bool                      `json:"large"`
	DBStats             sql.DBStats               `json:"db_stats"`
	Checkpoint          *SQLiteWALCheckpointStats `json:"checkpoint,omitempty"`
}

type SQLiteWALCheckpointStats struct {
	Mode              SQLiteWALCheckpointMode `json:"mode"`
	Busy              int                     `json:"busy"`
	LogFrames         int                     `json:"log_frames"`
	CheckpointedFrame int                     `json:"checkpointed_frames"`
	WALBytesBefore    int64                   `json:"wal_bytes_before"`
	WALBytesAfter     int64                   `json:"wal_bytes_after"`
	Duration          time.Duration           `json:"duration"`
}

func (c *Client) SQLiteWALDiagnostics(ctx context.Context) (SQLiteWALDiagnostics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := c.dbHandle()
	if err != nil {
		return SQLiteWALDiagnostics{}, err
	}
	walBytes, err := sqliteWALSize(c.dbPath)
	if err != nil {
		return SQLiteWALDiagnostics{}, c.wrapError("sqlite-wal-diagnostics", "", err)
	}
	return SQLiteWALDiagnostics{
		DBPath:              c.dbPath,
		WALPath:             sqliteWALPath(c.dbPath),
		WALBytes:            walBytes,
		CheckpointThreshold: sqliteWALCheckpointThreshold,
		LargeThreshold:      sqliteWALLargeThreshold,
		Large:               walBytes >= sqliteWALLargeThreshold,
		DBStats:             db.Stats(),
	}, nil
}

func (c *Client) CheckpointSQLiteWAL(ctx context.Context, mode SQLiteWALCheckpointMode) (SQLiteWALCheckpointStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := c.dbHandle()
	if err != nil {
		return SQLiteWALCheckpointStats{}, err
	}
	var stats SQLiteWALCheckpointStats
	err = sqliteutil.WithWriteLockContext(ctx, c.dbPath, func(lockCtx context.Context) error {
		var checkpointErr error
		stats, checkpointErr = c.checkpointSQLiteWALLocked(lockCtx, db, mode)
		return checkpointErr
	})
	return stats, err
}

func (c *Client) checkpointSQLiteWALLocked(ctx context.Context, db *sql.DB, mode SQLiteWALCheckpointMode) (SQLiteWALCheckpointStats, error) {
	mode = normalizeSQLiteWALCheckpointMode(mode)
	before, err := sqliteWALSize(c.dbPath)
	if err != nil {
		return SQLiteWALCheckpointStats{}, c.wrapError("sqlite-wal-checkpoint", "", err)
	}
	startedAt := time.Now()
	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("PRAGMA wal_checkpoint(%s)", mode)).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return SQLiteWALCheckpointStats{}, c.wrapError("sqlite-wal-checkpoint", "", err)
	}
	after, err := sqliteWALSize(c.dbPath)
	if err != nil {
		return SQLiteWALCheckpointStats{}, c.wrapError("sqlite-wal-checkpoint", "", err)
	}
	stats := SQLiteWALCheckpointStats{
		Mode:              mode,
		Busy:              busy,
		LogFrames:         logFrames,
		CheckpointedFrame: checkpointedFrames,
		WALBytesBefore:    before,
		WALBytesAfter:     after,
		Duration:          time.Since(startedAt),
	}
	c.logSQLiteWALCheckpoint(ctx, stats)
	return stats, nil
}

func (c *Client) maybeMaintainSQLiteWAL(ctx context.Context) {
	if c == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	c.walMu.Lock()
	if !c.lastWALCheckAt.IsZero() && now.Sub(c.lastWALCheckAt) < sqliteWALMaintenanceInterval {
		c.walMu.Unlock()
		return
	}
	c.lastWALCheckAt = now
	c.walMu.Unlock()

	walBytes, err := sqliteWALSize(c.dbPath)
	if err != nil {
		if c.logger != nil {
			c.logger.WarnContext(ctx, "sqlite wal stat failed", "db_path", c.dbPath, "wal_path", sqliteWALPath(c.dbPath), "error", err)
		}
		return
	}
	if walBytes < sqliteWALCheckpointThreshold {
		return
	}
	if c.logger != nil {
		c.logger.WarnContext(ctx, "sqlite wal exceeded checkpoint threshold",
			"db_path", c.dbPath,
			"wal_path", sqliteWALPath(c.dbPath),
			"wal_bytes", walBytes,
			"checkpoint_threshold_bytes", sqliteWALCheckpointThreshold,
			"large_threshold_bytes", sqliteWALLargeThreshold,
			"large", walBytes >= sqliteWALLargeThreshold,
		)
	}
	checkpointCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := c.CheckpointSQLiteWAL(checkpointCtx, SQLiteWALCheckpointPassive); err != nil && c.logger != nil {
		c.logger.WarnContext(ctx, "sqlite passive wal checkpoint failed",
			"db_path", c.dbPath,
			"wal_path", sqliteWALPath(c.dbPath),
			"wal_bytes", walBytes,
			"error", err,
		)
	}
}

func (c *Client) logSQLiteWALCheckpoint(ctx context.Context, stats SQLiteWALCheckpointStats) {
	if c == nil || c.logger == nil {
		return
	}
	level := slogLevelInfo
	if stats.Busy != 0 || stats.WALBytesAfter >= sqliteWALLargeThreshold {
		level = slogLevelWarn
	}
	attrs := []any{
		"event", "sqlite.wal_checkpoint.completed",
		"db_path", c.dbPath,
		"wal_path", sqliteWALPath(c.dbPath),
		"mode", string(stats.Mode),
		"busy", stats.Busy,
		"log_frames", stats.LogFrames,
		"checkpointed_frames", stats.CheckpointedFrame,
		"wal_bytes_before", stats.WALBytesBefore,
		"wal_bytes_after", stats.WALBytesAfter,
		"duration_ms", stats.Duration.Milliseconds(),
	}
	if level == slogLevelWarn {
		c.logger.WarnContext(ctx, "sqlite wal checkpoint completed", attrs...)
		return
	}
	c.logger.InfoContext(ctx, "sqlite wal checkpoint completed", attrs...)
}

type sqliteLogLevel int

const (
	slogLevelInfo sqliteLogLevel = iota
	slogLevelWarn
)

func normalizeSQLiteWALCheckpointMode(mode SQLiteWALCheckpointMode) SQLiteWALCheckpointMode {
	switch SQLiteWALCheckpointMode(strings.ToUpper(strings.TrimSpace(string(mode)))) {
	case SQLiteWALCheckpointTruncate:
		return SQLiteWALCheckpointTruncate
	default:
		return SQLiteWALCheckpointPassive
	}
}

func sqliteWALPath(dbPath string) string {
	return dbPath + "-wal"
}

func sqliteWALSize(dbPath string) (int64, error) {
	info, err := os.Stat(sqliteWALPath(dbPath))
	if err == nil {
		return info.Size(), nil
	}
	if os.IsNotExist(err) {
		return 0, nil
	}
	return 0, err
}
