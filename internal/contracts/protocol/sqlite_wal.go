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
	Checkpoint          *TaskSQLiteWALCheckpointInfo `json:"checkpoint,omitempty" msgpack:"checkpoint,omitempty"`
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
