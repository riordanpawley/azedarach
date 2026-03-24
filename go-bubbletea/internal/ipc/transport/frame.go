package transport

import (
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type frameType string

const (
	frameTypeHello    frameType = "hello"
	frameTypeHelloAck frameType = "hello_ack"
	frameTypeCommand  frameType = "command"
	frameTypeResponse frameType = "response"
	frameTypeSubscribe frameType = "subscribe"
	frameTypeEvent    frameType = "event"
	frameTypeError    frameType = "error"
)

type subscribeRequest struct {
	ProjectID    string `msgpack:"project_id"`
	FromRevision uint64 `msgpack:"from_revision"`
}

type rpcFrame struct {
	Type      frameType                  `msgpack:"type"`
	Hello     *protocol.Hello            `msgpack:"hello,omitempty"`
	HelloAck  *protocol.HelloAck         `msgpack:"hello_ack,omitempty"`
	Request   *protocol.RequestEnvelope  `msgpack:"request,omitempty"`
	Response  *protocol.ResponseEnvelope `msgpack:"response,omitempty"`
	Subscribe *subscribeRequest          `msgpack:"subscribe,omitempty"`
	Event     *protocol.EventEnvelope    `msgpack:"event,omitempty"`
	Error     *protocol.ErrorEnvelope    `msgpack:"error,omitempty"`
}
