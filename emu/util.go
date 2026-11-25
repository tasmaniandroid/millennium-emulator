// Package emu implements the complete emulated payphone: an instruction-
// accurate Z80/Z180 CPU core, the Z180 MMU, board profiles (MTR1/CALA,
// MTR2/TTY) and peripheral device models: character VFD, Track 2 and GPM103-style smart-
// card readers, serial coin validator, bit-serial SSI modem, Microwire
// EEPROM, AMD-command-compatible flash, DMA channels, programmable timers
// and the clocked telephony message interface.
//
// Wire contract: JSON field order, error message text, undocumented flag
// bits and cycle counts are observable API surface — snapshots, the recorded
// event log and the browser protocol all depend on them, and the differential
// harnesses in the parent directory (diffcheck.mjs, ncccheck.mjs) enforce
// them across server instances. Comments that explain a strange-looking
// construct mark deliberate behavior; do not simplify such code without
// re-running those harnesses.
//
// Timing model: every instruction consumes approximate Z80 T-states (not
// cycle-exact pin timing). Peripheral activity is driven from Tick(t),
// called after each instruction and each interrupt acceptance, so device
// timing granularity is one instruction.
package emu

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const MaxSafeInteger = int64(1)<<53 - 1

// Hash returns the lowercase SHA-256 hex digest of data.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// APIError is any emulation/validation error; the server reports every such
// error as HTTP 400 {"error": message}.
type APIError struct{ Message string }

func (e *APIError) Error() string { return e.Message }

func errf(format string, args ...any) error {
	return &APIError{Message: fmt.Sprintf(format, args...)}
}

func errPlain(msg string) error { return &APIError{Message: msg} }

// Err builds an API error from another package.
func Err(msg string) error { return &APIError{Message: msg} }

var hexIntRe = regexp.MustCompile(`(?i)^(?:0x[0-9a-f]+|[0-9]+)$`)

// Number validates one decoded-JSON value (float64, string, bool or nil) as
// an integer in [min,max]. Decimal and 0x-prefixed hex strings are accepted,
// booleans coerce to 1/0, everything else fails. The error text is part of
// the API contract.
func Number(value any, name string, min, max int64) (int64, error) {
	rangErr := errf("%s must be an integer %d..%d", name, min, max)
	var n float64
	switch v := value.(type) {
	case nil:
		return 0, rangErr
	case float64:
		n = v
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, rangErr
		}
		n = f
	case string:
		if !hexIntRe.MatchString(v) {
			return 0, errf("Invalid %s", name)
		}
		s := v
		base := 10
		if len(s) > 2 && strings.EqualFold(s[:2], "0x") {
			s, base = s[2:], 16
		}
		u, err := strconv.ParseUint(s, base, 64)
		if err != nil {
			return 0, rangErr
		}
		n = float64(u)
	case bool:
		// Number(true)===1, Number(false)===0 in JS.
		if v {
			n = 1
		}
	default:
		return 0, rangErr
	}
	if n != math.Trunc(n) || math.IsNaN(n) || math.IsInf(n, 0) || n > float64(MaxSafeInteger) || n < -float64(MaxSafeInteger) {
		return 0, rangErr
	}
	i := int64(n)
	if i < min || i > max {
		return 0, rangErr
	}
	return i, nil
}

var wsRe = regexp.MustCompile(`\s`)
var evenHexRe = regexp.MustCompile(`(?i)^(?:[0-9a-f]{2})+$`)

// Bytes decodes a whitespace-tolerant, even-length hexadecimal string. The
// error text is part of the API contract.
func Bytes(hexStr any) ([]byte, error) {
	s, ok := hexStr.(string)
	if !ok {
		return nil, errors.New("Expected nonempty even-length hexadecimal bytes")
	}
	s = wsRe.ReplaceAllString(s, "")
	if !evenHexRe.MatchString(s) {
		return nil, errors.New("Expected nonempty even-length hexadecimal bytes")
	}
	return hex.DecodeString(s)
}

// ByteList serializes []byte as a JSON array of numbers, matching how
// JS Array/Uint8Array-spread device fields appear in snapshots.
type ByteList []byte

func (b ByteList) MarshalJSON() ([]byte, error) {
	if len(b) == 0 {
		return []byte("[]"), nil
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, v := range b {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Itoa(int(v)))
	}
	sb.WriteByte(']')
	return []byte(sb.String()), nil
}

func (b *ByteList) UnmarshalJSON(data []byte) error {
	var raw []float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(ByteList, len(raw))
	for i, f := range raw {
		out[i] = byte(int64(f))
	}
	*b = out
	return nil
}

// Ordered builds a JSON object preserving key insertion order; event and
// state payloads depend on exact key order.
func Ordered(pairs ...any) json.RawMessage {
	var sb strings.Builder
	sb.WriteByte('{')
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			sb.WriteByte(',')
		}
		k, _ := pairs[i].(string)
		kb, _ := json.Marshal(k)
		sb.Write(kb)
		sb.WriteByte(':')
		vb, err := json.Marshal(pairs[i+1])
		if err != nil {
			vb = []byte("null")
		}
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return json.RawMessage(sb.String())
}

// MergeJSON concatenates object bodies: {"a":1} + {"b":2} -> {"a":1,"b":2}.
// An empty/nil tail is dropped, mirroring {...a, ...{}}.
func MergeJSON(parts ...json.RawMessage) json.RawMessage {
	var inner []string
	for _, p := range parts {
		s := strings.TrimSpace(string(p))
		if s == "" || s == "{}" || s == "null" {
			continue
		}
		inner = append(inner, strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}"))
	}
	return json.RawMessage("{" + strings.Join(inner, ",") + "}")
}

// JSNum renders a float for API payloads: whole values print as integers.
func JSNum(f float64) json.RawMessage {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return json.RawMessage(strconv.FormatInt(int64(f), 10))
	}
	b, _ := json.Marshal(f)
	// Go emits e+21 style like JS for extremes; otherwise identical.
	return b
}
