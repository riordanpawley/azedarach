package protocol

// Hello is the initial client handshake request.
type Hello struct {
	ProtocolVersion Version  `json:"protocol_version" msgpack:"protocol_version"`
	ClientName      string   `json:"client_name" msgpack:"client_name"`
	ClientVersion   string   `json:"client_version" msgpack:"client_version"`
	Capabilities    []string `json:"capabilities,omitempty" msgpack:"capabilities,omitempty"`
}

// HelloAck is the daemon handshake response.
type HelloAck struct {
	Accepted              bool    `json:"accepted" msgpack:"accepted"`
	DaemonProtocolVersion Version `json:"daemon_protocol_version" msgpack:"daemon_protocol_version"`
	DaemonVersion         string  `json:"daemon_version" msgpack:"daemon_version"`
	Reason                string  `json:"reason,omitempty" msgpack:"reason,omitempty"`
}
