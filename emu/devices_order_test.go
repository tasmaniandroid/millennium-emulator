package emu

import (
	"bytes"
	"encoding/json"
	"testing"
)

// expectedDevicesKeyOrder is the canonical devices field order. Snapshot and
// state JSON must keep this exact order: servers are compared byte-for-byte
// by the differential harnesses.
var expectedDevicesKeyOrder = []string{
	"display", "cursor", "displayMode", "displayCursorVisible", "displayScrollPending",
	"displayBlinkPositions", "displayIntensity", "displayRevision", "displayPending",
	"customRemaining", "displayCustomCode", "displayCustomData", "displayCustomCharacters",
	"eeprom", "eeBits", "eeOut", "eeRead", "eeWriteEnabled", "eeData", "eeAddress",
	"coinUART", "flashCommands",
	"offHook", "lineConnected", "reversed", "ring", "coverOpen", "cashboxRemoved",
	"ipQueue", "ipTx", "ipRxActive", "asciRx", "asciNextRx", "asciNextTx", "asciTx",
	"coinTx", "coinCommand", "coinAccept", "coinEnabled", "coinEscrow", "coinCollected",
	"coinReturned", "card", "reader", "modem", "dma0Interrupt", "dma1Interrupt",
	"dialed", "callState", "serialPending", "serialEdges", "csioInterrupt",
	"timerAccum", "timerRead", "externalAccum", "externalPending", "telephonyResetDue",
	"scheduled", "voice", "unknownCommands", "management",
}

func devicesKeys(t *testing.T, d *Devices) []string {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	depth := 0
	expectKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
				if depth == 1 {
					expectKey = true
				}
			case '}', ']':
				depth--
				if depth == 0 {
					return keys
				}
				if depth == 1 {
					expectKey = true
				}
			}
			continue
		}
		if depth == 1 {
			if expectKey {
				if s, ok := tok.(string); ok {
					keys = append(keys, s)
				}
			}
			expectKey = !expectKey
		}
	}
	return keys
}

func TestDevicesKeyOrder(t *testing.T) {
	m := NewMachine("mtr1")
	keys := devicesKeys(t, m.Devices)
	if len(keys) != len(expectedDevicesKeyOrder) {
		t.Fatalf("key count %d != %d\ngot: %v", len(keys), len(expectedDevicesKeyOrder), keys)
	}
	for i, k := range expectedDevicesKeyOrder {
		if keys[i] != k {
			t.Fatalf("key %d: got %q want %q", i, keys[i], k)
		}
	}
}

// TestDeviceFieldOrderCoversAllFields guards against drift between the
// Devices struct and the restore validation table: every key must either be
// in deviceFieldOrder or be one of the object/nullable fields the JS entries
// loop skips (validated separately or dynamically).
func TestDeviceFieldOrderCoversAllFields(t *testing.T) {
	m := NewMachine("mtr2")
	keys := devicesKeys(t, m.Devices)
	listed := map[string]bool{}
	for _, f := range deviceFieldOrder {
		listed[f.name] = true
	}
	skipped := map[string]bool{
		"displayCustomCharacters": true, "card": true,
		"reader": true, "modem": true, "coinUART": true,
		// null-typed in the default state; JS validates these only when
		// the CURRENT machine has a non-null value (dynamic typeof check).
		"displayCustomCode": true, "telephonyResetDue": true,
	}
	for _, k := range keys {
		if !listed[k] && !skipped[k] {
			t.Fatalf("devices key %q is missing from deviceFieldOrder (restore validation would skip it)", k)
		}
	}
	for name := range listed {
		found := false
		for _, k := range keys {
			if k == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("deviceFieldOrder entry %q no longer exists on Devices", name)
		}
	}
}
