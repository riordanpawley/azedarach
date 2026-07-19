package protocol

// Version identifies the protocol contract version exchanged by daemon/client.
type Version uint16

const (
	// CurrentVersion is the latest protocol contract supported by this build.
	CurrentVersion Version = 58
)

// DaemonExecutablePreflight is the machine-readable compatibility contract
// emitted by azd before a replacement mutates the live daemon lifecycle.
type DaemonExecutablePreflight struct {
	DaemonVersion      string  `json:"daemon_version"`
	MinProtocolVersion Version `json:"min_protocol_version"`
	MaxProtocolVersion Version `json:"max_protocol_version"`
}

// CurrentDaemonExecutablePreflight describes the protocol range accepted by
// this daemon executable. The range is explicit so rolling upgrades can widen
// it later without weakening replacement admission into a version-string test.
func CurrentDaemonExecutablePreflight(daemonVersion string) DaemonExecutablePreflight {
	return DaemonExecutablePreflight{
		DaemonVersion:      daemonVersion,
		MinProtocolVersion: CurrentVersion,
		MaxProtocolVersion: CurrentVersion,
	}
}

// Accepts reports whether the executable can serve the requested protocol.
func (p DaemonExecutablePreflight) Accepts(version Version) bool {
	return p.MinProtocolVersion > 0 &&
		p.MinProtocolVersion <= p.MaxProtocolVersion &&
		version >= p.MinProtocolVersion &&
		version <= p.MaxProtocolVersion
}
