package haraft

import (
	"bytes"
	"encoding/base64"
	"encoding/json"

	"github.com/hashicorp/go-msgpack/v2/codec"
)

// encodeRPC serializes a hashicorp/raft RPC struct using msgpack — the
// same codec hashicorp/raft uses internally.
func encodeRPC(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	h := &codec.MsgpackHandle{}
	enc := codec.NewEncoder(&buf, h)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeRPC is the inverse of encodeRPC.
func decodeRPC(data []byte, v interface{}) error {
	h := &codec.MsgpackHandle{}
	dec := codec.NewDecoder(bytes.NewReader(data), h)
	return dec.Decode(v)
}

// encodeEnvelope produces the base64 string stored as a ConfigMap data value.
func encodeEnvelope(e RaftEnvelope) (string, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// decodeEnvelope is the inverse of encodeEnvelope.
func decodeEnvelope(s string) (RaftEnvelope, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return RaftEnvelope{}, err
	}
	var e RaftEnvelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return RaftEnvelope{}, err
	}
	return e, nil
}
