package emu

import (
	"encoding/base64"
	"encoding/json"
	"math"
)

// --- map helpers -------------------------------------------------------

func asArray(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

func isByteVal(v any) bool {
	f, ok := v.(float64)
	return ok && f == math.Trunc(f) && f >= 0 && f <= 255
}

func isSafeInt(v any) bool {
	f, ok := v.(float64)
	return ok && f == math.Trunc(f) && math.Abs(f) <= float64(MaxSafeInteger)
}

func isFiniteNum(v any) bool {
	f, ok := v.(float64)
	return ok && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func b64Field(m map[string]json.RawMessage, key string) ([]byte, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Accept padded and unpadded base64 alike.
		out, err = base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			return nil, false
		}
	}
	return out, true
}

// restoreVFDDefaults completes saved VFD fields with defaults, including the
// legacy migration for snapshots saved before the parser fields were split
// (a single mode byte then encoded cursor visibility and blink parameters).
func restoreVFDDefaults(saved map[string]any) map[string]any {
	d := map[string]any{}
	// vfdState() defaults
	display := make([]any, VFDSize)
	for i := range display {
		display[i] = float64(32)
	}
	d["display"] = display
	d["cursor"] = float64(0)
	d["displayMode"] = float64(VFDNormal)
	d["displayCursorVisible"] = false
	d["displayScrollPending"] = false
	d["displayBlinkPositions"] = []any{}
	d["displayIntensity"] = float64(0)
	d["displayRevision"] = float64(0)
	d["displayPending"] = float64(0)
	d["customRemaining"] = float64(0)
	d["displayCustomCode"] = nil
	d["displayCustomData"] = []any{}
	d["displayCustomCharacters"] = map[string]any{}
	for k, v := range saved {
		d[k] = v
	}
	_, hasRevision := saved["displayRevision"]
	if !hasRevision {
		oldModeAny := saved["displayMode"]
		oldMode, isNum := oldModeAny.(float64)
		mode := float64(VFDNormal)
		if isNum && (oldMode == VFDNormal || oldMode == VFDVertical || oldMode == VFDHorizontal) {
			mode = oldMode
		}
		d["displayMode"] = mode
		d["displayCursorVisible"] = isNum && oldMode == 0x13
		cr, _ := d["customRemaining"].(float64)
		if isNum && oldMode == 0x16 && cr != 0 {
			if cr == 2 {
				d["displayPending"] = float64(0x16)
			} else {
				d["displayPending"] = float64(0)
			}
			d["customRemaining"] = float64(0)
		}
	}
	return d
}

func defaultReaderMap() map[string]any {
	return map[string]any{"kind": nil, "position": "out", "track2": "", "image": []any{},
		"writeProtected": false, "samples": []any{}, "nextSample": float64(0),
		"samplePeriod": float64(3072), "finishCycle": float64(0), "finishPosition": "out",
		"address": float64(0), "resetClocked": false, "writeArmed": false, "writeStart": nil,
		"lastWriteAddress": float64(-1), "writeCount": float64(0), "reads": float64(0),
		"writes": float64(0), "dmaSamples": float64(0)}
}

func defaultModemMap() map[string]any {
	return map[string]any{"controls": []any{float64(0), float64(0), float64(0), float64(0)},
		"writeBits": []any{}, "readValue": float64(0), "readIndex": float64(0),
		"dialTone": true, "busy": false, "answerTone": false, "carrier": false,
		"dtmf": nil, "dtmfUntil": float64(0), "digits": "", "lastDigitCycle": float64(0),
		"callGeneration": float64(0)}
}
