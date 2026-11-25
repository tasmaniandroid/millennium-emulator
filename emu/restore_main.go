package emu

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// deviceFieldKind classifies Devices fields for the entries validation loop.
type deviceFieldKind int

const (
	fkArray deviceFieldKind = iota
	fkNumber
	fkBool
	fkString
)

// deviceFieldOrder lists the Devices fields in wire order for the snapshot
// restore type sweep. Object/null-typed fields (card, reader,
// modem, coinUART, displayCustomCode, telephonyResetDue,
// displayCustomCharacters) are validated separately or dynamically.
var deviceFieldOrder = []struct {
	name string
	kind deviceFieldKind
}{
	{"display", fkArray}, {"cursor", fkNumber}, {"displayMode", fkNumber},
	{"displayCursorVisible", fkBool}, {"displayScrollPending", fkBool},
	{"displayBlinkPositions", fkArray}, {"displayIntensity", fkNumber},
	{"displayRevision", fkNumber}, {"displayPending", fkNumber},
	{"customRemaining", fkNumber}, {"displayCustomData", fkArray},
	{"eeprom", fkArray}, {"eeBits", fkArray}, {"eeOut", fkArray},
	{"eeRead", fkBool}, {"eeWriteEnabled", fkBool}, {"eeData", fkArray},
	{"eeAddress", fkNumber}, {"flashCommands", fkArray},
	{"offHook", fkBool}, {"lineConnected", fkBool}, {"reversed", fkBool},
	{"ring", fkBool}, {"coverOpen", fkBool}, {"cashboxRemoved", fkBool},
	{"ipQueue", fkArray}, {"ipTx", fkArray}, {"ipRxActive", fkArray},
	{"asciRx", fkArray}, {"asciNextRx", fkArray}, {"asciNextTx", fkArray},
	{"asciTx", fkArray}, {"coinTx", fkArray}, {"coinCommand", fkArray},
	{"coinAccept", fkBool}, {"coinEnabled", fkArray}, {"coinEscrow", fkArray},
	{"coinCollected", fkArray}, {"coinReturned", fkArray},
	{"dma0Interrupt", fkBool}, {"dma1Interrupt", fkBool},
	{"dialed", fkString}, {"callState", fkNumber}, {"serialPending", fkNumber},
	{"serialEdges", fkNumber}, {"csioInterrupt", fkBool},
	{"timerAccum", fkNumber}, {"timerRead", fkBool}, {"externalAccum", fkNumber},
	{"externalPending", fkBool}, {"scheduled", fkArray}, {"voice", fkArray},
	{"unknownCommands", fkArray}, {"management", fkString},
}

// cpuFieldOrder lists the CPUState fields in wire order with their ranges.
var cpuFieldOrder = []struct {
	name   string
	max    int64
	isBool bool
}{
	{"a", 255, false}, {"f", 255, false}, {"b", 255, false}, {"c", 255, false},
	{"d", 255, false}, {"e", 255, false}, {"h", 255, false}, {"l", 255, false},
	{"a2", 255, false}, {"f2", 255, false}, {"b2", 255, false}, {"c2", 255, false},
	{"d2", 255, false}, {"e2", 255, false}, {"h2", 255, false}, {"l2", 255, false},
	{"ix", 65535, false}, {"iy", 65535, false}, {"sp", 65535, false}, {"pc", 65535, false},
	{"i", 255, false}, {"r", 255, false},
	{"iff1", 0, true}, {"iff2", 0, true},
	{"im", 2, false}, {"halted", 0, true}, {"eiDelay", 2, false},
	{"cycles", MaxSafeInteger, false}, {"instructions", MaxSafeInteger, false},
}

// Restore validates and applies a snapshot. Validation is exhaustive and
// ATOMIC: every check runs against the parsed JSON before any machine state
// is touched, and scripts and tests depend on the exact error messages.
// Legacy snapshots missing newer fields get device defaults;
// a snapshot lacking displayRevision also gets the old single-mode-byte
// migration (see restoreVFDDefaults). On success the machine stops paused
// with stopReason "snapshot restored" and a recorded event.
func (m *Machine) Restore(raw json.RawMessage) error {
	var sRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sRaw); err != nil {
		return errPlain("Incomplete snapshot")
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		return errPlain("Incomplete snapshot")
	}

	// version / board / romHash
	ver, verOK := s["version"].(float64)
	board := "mtr1"
	if b, ok := s["board"].(string); ok {
		board = b
	} else if _, present := s["board"]; present && s["board"] != nil {
		board = ""
	}
	romHash, _ := s["romHash"].(string)
	if !verOK || (ver != 1 && ver != 2) || board != m.Board.ID || romHash != Hash(m.ROM) {
		return errPlain("Snapshot does not match loaded ROM or board")
	}

	// flash
	var flash []byte
	flashOK := false
	if m.Board.Family == "mtr2" {
		if f, ok := s["flash"].(string); ok {
			flash, _ = base64DecodeLenient(f)
			flashOK = flash != nil
		}
		if !flashOK || len(flash) != 0x100000 {
			return errPlain("Invalid snapshot flash")
		}
	}

	// memory / ports
	mem, memOK := b64Field(sRaw, "memory")
	ports, portsOK := b64Field(sRaw, "ports")
	if !memOK || !portsOK {
		return errPlain(`The "data" argument must be specified`)
	}
	if len(mem) != 0x100000 || len(ports) != 256 {
		return errPlain("Invalid snapshot memory")
	}

	// ROM prefix
	if m.Board.Family == "mtr1" && !bytes.Equal(mem[:len(m.ROM)], m.ROM) {
		return errPlain("Snapshot ROM bytes do not match loaded ROM")
	}

	// presence
	cpuMap, cpuOK := s["cpu"].(map[string]any)
	devs, devsOK := s["devices"].(map[string]any)
	inputLog, logOK := asArray(s["inputLog"])
	if !cpuOK || s["cpu"] == nil || !devsOK || s["devices"] == nil || !logOK {
		return errPlain("Incomplete snapshot")
	}

	// merge defaults (structuredClone + ??= semantics)
	devs = restoreVFDDefaults(devs)
	if devs["reader"] == nil {
		devs["reader"] = defaultReaderMap()
	}
	modem := defaultModemMap()
	if savedModem, ok := devs["modem"].(map[string]any); ok {
		for k, v := range savedModem {
			modem[k] = v
		}
	}
	devs["modem"] = modem
	if devs["dma0Interrupt"] == nil {
		devs["dma0Interrupt"] = false
	}
	if devs["dma1Interrupt"] == nil {
		devs["dma1Interrupt"] = false
	}
	if devs["coinUART"] == nil {
		regs := make([]any, 8)
		for i := range regs {
			regs[i] = float64(0)
		}
		devs["coinUART"] = map[string]any{"registers": regs, "divisor": float64(0x280),
			"nextRX": float64(0), "nextTX": float64(0)}
	}
	if devs["flashCommands"] == nil {
		devs["flashCommands"] = []any{
			map[string]any{"phase": float64(0), "id": false},
			map[string]any{"phase": float64(0), "id": false}}
	}
	if devs["asciNextTx"] == nil {
		devs["asciNextTx"] = []any{float64(0), float64(0)}
	}

	// CPU fields
	for _, f := range cpuFieldOrder {
		v := cpuMap[f.name]
		if f.isBool {
			if _, ok := v.(bool); !ok {
				return errf("Invalid snapshot CPU %s", f.name)
			}
			continue
		}
		if _, ok := v.(float64); !ok {
			return errf("Invalid snapshot CPU %s", f.name)
		}
		if _, err := Number(v, fmt.Sprintf("CPU %s", f.name), 0, f.max); err != nil {
			return err
		}
	}

	// devices entries loop
	for _, f := range deviceFieldOrder {
		saved := devs[f.name]
		bad := false
		switch f.kind {
		case fkArray:
			a, ok := asArray(saved)
			bad = !ok || len(a) > 10000
		case fkNumber:
			bad = !isFiniteNum(saved)
		case fkBool:
			_, ok := saved.(bool)
			bad = !ok
		case fkString:
			_, ok := saved.(string)
			bad = !ok
		}
		if bad {
			return errf("Invalid snapshot device %s", f.name)
		}
	}

	// display / eeprom contents
	for _, spec := range []struct {
		name   string
		length int
	}{{"display", 40}, {"eeprom", m.Board.EeWords * 2}} {
		arr, _ := asArray(devs[spec.name])
		if len(arr) != spec.length {
			return errf("Invalid snapshot %s", spec.name)
		}
		for _, v := range arr {
			if !isByteVal(v) {
				return errf("Invalid snapshot %s", spec.name)
			}
		}
	}

	// coinUART
	uart, _ := devs["coinUART"].(map[string]any)
	regs, regsOK := asArray(uart["registers"])
	if uart == nil || !regsOK || len(regs) != 8 {
		return errPlain("Invalid snapshot coin UART")
	}
	for _, v := range regs {
		if !isByteVal(v) {
			return errPlain("Invalid snapshot coin UART")
		}
	}
	if _, err := Number(uart["divisor"], "snapshot UART divisor", 0, 65535); err != nil {
		return err
	}
	if _, err := Number(uart["nextRX"], "snapshot UART nextRX", 0, MaxSafeInteger); err != nil {
		return err
	}
	if _, err := Number(uart["nextTX"], "snapshot UART nextTX", 0, MaxSafeInteger); err != nil {
		return err
	}

	// flashCommands
	fcs, _ := asArray(devs["flashCommands"])
	if len(fcs) != 2 {
		return errPlain("Invalid snapshot flash command state")
	}
	for _, fc := range fcs {
		fcm, ok := fc.(map[string]any)
		if !ok || fcm == nil {
			return errPlain("Invalid snapshot flash command state")
		}
		phaseOK := false
		switch ph := fcm["phase"].(type) {
		case float64:
			phaseOK = ph == 0 || ph == 1 || ph == 2 || ph == 3 || ph == 4 || ph == 5
		case string:
			phaseOK = ph == "program"
		}
		if _, idOK := fcm["id"].(bool); !phaseOK || !idOK {
			return errPlain("Invalid snapshot flash command state")
		}
	}

	return m.restoreFinish(devs, cpuMap, mem, ports, flash, inputLog, sRaw)
}
