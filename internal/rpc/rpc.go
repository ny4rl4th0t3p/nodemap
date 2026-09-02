// Package rpc reads the two CometBFT RPC endpoints the crawl needs and
// decodes them into transient, deliberately narrow types.
//
// Responses come from strangers, so every read is bounded and every decode
// is expected to fail gracefully. The types below are also part of the
// security boundary: a peer has no id field, a status reduces validator_info
// to one boolean without ever decoding the address or key, and nothing
// carries connection direction or statistics. encoding/json drops what the
// struct cannot hold, so those values never exist in this process as
// anything but bytes on the wire.
package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// MaxBody bounds a single response body. A 57-peer Cosmos Hub net_info is
// about 106 KB; 4 MiB leaves room for very large peer sets and rejects
// anything absurd before it is buffered.
const MaxBody = 4 << 20

// maxErrorMessage truncates a JSON-RPC error message from a stranger before
// it is wrapped into a Go error.
const maxErrorMessage = 100

var (
	// ErrBodyTooLarge is returned when a response exceeds MaxBody.
	ErrBodyTooLarge = errors.New("rpc: response body exceeds limit")
	// ErrHTTPStatus is returned for any non-200 response, including
	// redirects, which are never followed.
	ErrHTTPStatus = errors.New("rpc: non-200 status")
	// ErrRPCError is returned when the JSON-RPC envelope carries an error.
	ErrRPCError = errors.New("rpc: JSON-RPC error")
	// ErrEmptyResult is returned when the envelope has neither result nor error.
	ErrEmptyResult = errors.New("rpc: empty result")
)

// NodeInfo is the subset of node_info kept for a peer. There is no id.
type NodeInfo struct {
	// ListenAddr is the advertised P2P address, usually "tcp://0.0.0.0:26656".
	ListenAddr string `json:"listen_addr"`
	// Network is the chain-id the node reports.
	Network string `json:"network"`
	// Version is the CometBFT version; read only to bump an aggregate counter.
	Version string `json:"version"`
	// The moniker is deliberately not decoded: nothing publishes it.
	Other Other `json:"other"`
}

// Other is node_info.other.
type Other struct {
	// TxIndex is "on" or "off".
	TxIndex string `json:"tx_index"`
	// RPCAddress is the RPC bind address, e.g. "tcp://0.0.0.0:26657".
	RPCAddress string `json:"rpc_address"`
}

// Peer is one entry of net_info.peers. Direction and connection statistics
// are not decoded; neither is the peer's node id.
type Peer struct {
	NodeInfo NodeInfo `json:"node_info"`
	// RemoteIP is the IP the responding node sees this peer at. It is used
	// as a dial target and as a transient enrichment input, then dropped.
	RemoteIP string `json:"remote_ip"`
}

// NetInfo is the decoded result of /net_info.
type NetInfo struct {
	Peers []Peer `json:"peers"`
}

// SelfInfo is the responding node's own node_info from /status. It carries
// the id because the suppression check hashes it; the id is compared and
// discarded, never persisted.
type SelfInfo struct {
	NodeInfo
	ID string `json:"id"`
}

// SyncInfo is the subset of sync_info kept.
type SyncInfo struct {
	LatestBlockHeight   int64 `json:"latest_block_height,string"`
	EarliestBlockHeight int64 `json:"earliest_block_height,string"`
	CatchingUp          bool  `json:"catching_up"`
}

// Status is the decoded result of /status. The validator identity has no
// field here: of validator_info, only whether voting_power is positive is
// read, into Validator, and the address and key are never decoded. The
// crawler uses Validator to keep a self-declared validator out of the
// endpoint directory; it is never persisted.
type Status struct {
	NodeInfo  SelfInfo `json:"node_info"`
	SyncInfo  SyncInfo `json:"sync_info"`
	Validator bool     `json:"-"`
}

// UnmarshalJSON decodes the narrow fields and reduces validator_info to the
// single boolean, in the same step, so no wider form ever exists.
func (s *Status) UnmarshalJSON(data []byte) error {
	type plain Status
	var aux struct {
		plain
		ValidatorInfo struct {
			VotingPower string `json:"voting_power"`
		} `json:"validator_info"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*s = Status(aux.plain)
	n, err := strconv.ParseInt(aux.ValidatorInfo.VotingPower, 10, 64)
	s.Validator = err == nil && n > 0
	return nil
}

type envelope struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// DecodeNetInfo reads a bounded JSON-RPC response body into a NetInfo.
func DecodeNetInfo(r io.Reader) (*NetInfo, error) {
	var v NetInfo
	if err := decodeEnvelope(r, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// DecodeStatus reads a bounded JSON-RPC response body into a Status.
func DecodeStatus(r io.Reader) (*Status, error) {
	var v Status
	if err := decodeEnvelope(r, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// decodeEnvelope buffers at most MaxBody bytes, unwraps the JSON-RPC
// envelope, and decodes the result into v.
func decodeEnvelope(r io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(r, MaxBody+1))
	if err != nil {
		return fmt.Errorf("rpc: read: %w", err)
	}
	if len(data) > MaxBody {
		return ErrBodyTooLarge
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("rpc: envelope: %w", err)
	}
	if env.Error != nil {
		msg := env.Error.Message
		if len(msg) > maxErrorMessage {
			msg = msg[:maxErrorMessage]
		}
		return fmt.Errorf("%w: code %d: %s", ErrRPCError, env.Error.Code, msg)
	}
	if len(env.Result) == 0 {
		return ErrEmptyResult
	}
	if err := json.Unmarshal(env.Result, v); err != nil {
		return fmt.Errorf("rpc: result: %w", err)
	}
	return nil
}
