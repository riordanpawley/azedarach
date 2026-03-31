package protocol

// Version identifies the protocol contract version exchanged by daemon/client.
type Version uint16

const (
	// CurrentVersion is the latest protocol contract supported by this build.
	CurrentVersion Version = 2
)
