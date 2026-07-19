package protocol

const CommandTaskSQLiteWAL = "task.sqlite_wal"

type TaskSQLiteWALRequest struct {
	CheckpointMode string `json:"checkpoint_mode,omitempty" msgpack:"checkpoint_mode,omitempty"`
}

type TaskSQLiteWALResponse struct {
	DBPath              string                       `json:"db_path" msgpack:"db_path"`
	WALPath             string                       `json:"wal_path" msgpack:"wal_path"`
	WALBytes            int64                        `json:"wal_bytes" msgpack:"wal_bytes"`
	CheckpointThreshold int64                        `json:"checkpoint_threshold_bytes" msgpack:"checkpoint_threshold_bytes"`
	LargeThreshold      int64                        `json:"large_threshold_bytes" msgpack:"large_threshold_bytes"`
	Large               bool                         `json:"large" msgpack:"large"`
	OpenConnections     int                          `json:"open_connections" msgpack:"open_connections"`
	InUse               int                          `json:"in_use" msgpack:"in_use"`
	Idle                int                          `json:"idle" msgpack:"idle"`
	Stores              []TaskSQLiteStoreInfo        `json:"stores" msgpack:"stores"`
	Checkpoint          *TaskSQLiteWALCheckpointInfo `json:"checkpoint,omitempty" msgpack:"checkpoint,omitempty"`
}

type TaskSQLiteStoreInfo struct {
	ProjectIDs                 []string `json:"project_ids" msgpack:"project_ids"`
	Owner                      string   `json:"owner" msgpack:"owner"`
	DBPath                     string   `json:"db_path" msgpack:"db_path"`
	Open                       bool     `json:"open" msgpack:"open"`
	MaxOpenConnections         int      `json:"max_open_connections" msgpack:"max_open_connections"`
	OpenConnections            int      `json:"open_connections" msgpack:"open_connections"`
	InUse                      int      `json:"in_use" msgpack:"in_use"`
	Idle                       int      `json:"idle" msgpack:"idle"`
	WaitCount                  int64    `json:"wait_count" msgpack:"wait_count"`
	WaitDurationMillisecond    int64    `json:"wait_duration_ms" msgpack:"wait_duration_ms"`
	MutationHolder             string   `json:"mutation_holder,omitempty" msgpack:"mutation_holder,omitempty"`
	MutationHeldMillisecond    int64    `json:"mutation_held_ms,omitempty" msgpack:"mutation_held_ms,omitempty"`
	SQLiteWriteHolder          string   `json:"sqlite_write_holder,omitempty" msgpack:"sqlite_write_holder,omitempty"`
	SQLiteWriteHeldMillisecond int64    `json:"sqlite_write_held_ms,omitempty" msgpack:"sqlite_write_held_ms,omitempty"`
	SQLiteWriteWaiters         int      `json:"sqlite_write_waiters,omitempty" msgpack:"sqlite_write_waiters,omitempty"`
	ProjectionWatchesActive    int64    `json:"projection_watches_active,omitempty" msgpack:"projection_watches_active,omitempty"`
	ProjectionWatchesStarted   uint64   `json:"projection_watches_started,omitempty" msgpack:"projection_watches_started,omitempty"`
	ProjectionWatchesDone      uint64   `json:"projection_watches_completed,omitempty" msgpack:"projection_watches_completed,omitempty"`
}

type TaskSQLiteWALCheckpointInfo struct {
	Mode                string `json:"mode" msgpack:"mode"`
	Busy                int    `json:"busy" msgpack:"busy"`
	LogFrames           int    `json:"log_frames" msgpack:"log_frames"`
	CheckpointedFrames  int    `json:"checkpointed_frames" msgpack:"checkpointed_frames"`
	WALBytesBefore      int64  `json:"wal_bytes_before" msgpack:"wal_bytes_before"`
	WALBytesAfter       int64  `json:"wal_bytes_after" msgpack:"wal_bytes_after"`
	DurationMillisecond int64  `json:"duration_ms" msgpack:"duration_ms"`
}
