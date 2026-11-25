package server

import (
	"encoding/json"
	"math"
	"strconv"

	"millennium/emu"
)

// apiResponse is the outcome of one API call.
type apiResponse struct {
	Status     int
	JSON       json.RawMessage
	Binary     []byte
	BinaryName string
	SSE        bool
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"error":"marshal failure"}`)
	}
	return b
}

func okResp(v any) (*apiResponse, error) {
	return &apiResponse{Status: 200, JSON: jsonOf(v)}, nil
}

// jsString stringifies any decoded-JSON value the way API error messages
// expect (unknown device-event types are echoed back verbatim).
func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return "undefined"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		b, _ := json.Marshal(t)
		return string(b)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func qNum(q map[string]string, key string, def float64, name string, min, max int64) (int64, error) {
	if v, ok := q[key]; ok {
		return emu.Number(v, name, min, max)
	}
	return emu.Number(def, name, min, max)
}

func spaceOf(v any, def string) (string, error) {
	if v == nil {
		v = def
	}
	s, ok := v.(string)
	if !ok || (s != "logical" && s != "physical") {
		return "", emu.Err("space must be logical or physical")
	}
	return s, nil
}

// rawOf returns the raw JSON of a body field when available.
func rawOf(bRaw map[string]json.RawMessage, b map[string]any, key string) json.RawMessage {
	if bRaw != nil {
		if r, ok := bRaw[key]; ok {
			return r
		}
	}
	if v, ok := b[key]; ok && v != nil {
		return jsonOf(v)
	}
	return nil
}

func nonNilBytes(b emu.ByteList) emu.ByteList {
	if b == nil {
		return emu.ByteList{}
	}
	return b
}
