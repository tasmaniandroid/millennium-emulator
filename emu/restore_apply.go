package emu

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

func base64DecodeLenient(s string) ([]byte, bool) {
	out, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return out, true
	}
	out, err = base64.RawStdEncoding.DecodeString(s)
	if err == nil {
		return out, true
	}
	return nil, false
}

// restoreFinish performs the VFD/serial/scheduled validations and applies
// the snapshot atomically.
func (m *Machine) restoreFinish(devs map[string]any, cpuMap map[string]any,
	mem, ports, flash []byte, inputLog []any, sRaw map[string]json.RawMessage) error {

	// cursor / displayRevision
	if _, err := Number(devs["cursor"], "display cursor", 0, 39); err != nil {
		return err
	}
	if _, err := Number(devs["displayRevision"], "display revision", 0, MaxSafeInteger); err != nil {
		return err
	}
	// VFD mode / parser state
	mode, _ := devs["displayMode"].(float64)
	pending, _ := devs["displayPending"].(float64)
	modeOK := mode == 17 || mode == 18 || mode == 21
	pendingOK := pending == 0 || pending == 22 || pending == 26 || pending == 27
	if !modeOK || !pendingOK {
		return errPlain("Invalid snapshot VFD mode/parser")
	}
	if _, err := Number(devs["customRemaining"], "font bytes", 0, 5); err != nil {
		return err
	}
	blinks, _ := asArray(devs["displayBlinkPositions"])
	for _, v := range blinks {
		f, ok := v.(float64)
		if !ok || f != float64(int(f)) || f < 0 || f > 39 {
			return errPlain("Invalid snapshot VFD parameters")
		}
	}
	customData, _ := asArray(devs["displayCustomData"])
	if len(customData) > 5 {
		return errPlain("Invalid snapshot VFD parameters")
	}
	for _, v := range customData {
		if !isByteVal(v) {
			return errPlain("Invalid snapshot VFD parameters")
		}
	}
	if devs["displayCustomCode"] != nil {
		if _, err := Number(devs["displayCustomCode"], "custom character", 0, 255); err != nil {
			return err
		}
	}
	chars, charsOK := devs["displayCustomCharacters"].(map[string]any)
	if _, isArr := devs["displayCustomCharacters"].([]any); isArr || !charsOK {
		return errPlain("Invalid snapshot VFD characters")
	}
	for code, data := range chars {
		if _, err := Number(code, "custom character", 0, 255); err != nil {
			return err
		}
		arr, ok := asArray(data)
		if !ok || len(arr) != 5 {
			return errPlain("Invalid snapshot VFD character data")
		}
		for _, v := range arr {
			if !isByteVal(v) {
				return errPlain("Invalid snapshot VFD character data")
			}
		}
	}
	// asciRx / asciTx
	for _, k := range []string{"asciRx", "asciTx"} {
		outer, ok := asArray(devs[k])
		if !ok || len(outer) != 2 {
			return errf("Invalid snapshot %s", k)
		}
		for _, inner := range outer {
			arr, ok := asArray(inner)
			if !ok {
				return errf("Invalid snapshot %s", k)
			}
			for _, v := range arr {
				if !isByteVal(v) {
					return errf("Invalid snapshot %s", k)
				}
			}
		}
	}
	// serial timers
	nextRx, ok := asArray(devs["asciNextRx"])
	if !ok || len(nextRx) != 2 {
		return errPlain("Invalid snapshot serial timers")
	}
	for _, v := range nextRx {
		if !isSafeInt(v) || v.(float64) < 0 {
			return errPlain("Invalid snapshot serial timers")
		}
	}
	nextTx, ok := asArray(devs["asciNextTx"])
	if !ok || len(nextTx) != 2 {
		return errPlain("Invalid snapshot transmit timers")
	}
	for _, v := range nextTx {
		if !isSafeInt(v) || v.(float64) < 0 {
			return errPlain("Invalid snapshot transmit timers")
		}
	}
	// ipQueue
	ipq, ok := asArray(devs["ipQueue"])
	if !ok {
		return errPlain("Invalid snapshot telephony queue")
	}
	for _, msg := range ipq {
		arr, ok := asArray(msg)
		if !ok {
			return errPlain("Invalid snapshot telephony queue")
		}
		for _, v := range arr {
			if !isByteVal(v) {
				return errPlain("Invalid snapshot telephony queue")
			}
		}
	}
	// scheduled
	sched, _ := asArray(devs["scheduled"])
	for _, e := range sched {
		em, ok := e.(map[string]any)
		if !ok {
			return errPlain("scheduled cycle must be an integer 0..9007199254740991")
		}
		if _, err := Number(em["cycle"], "scheduled cycle", 0, MaxSafeInteger); err != nil {
			return err
		}
		typ, _ := em["type"].(string)
		data := map[string]any{}
		if dm, ok := em["data"].(map[string]any); ok && dm != nil {
			data = dm
		}
		if err := ValidateInput(typ, data); err != nil {
			return err
		}
	}

	// --- apply ---
	copy(m.Memory, mem)
	if m.Board.Family == "mtr2" && flash != nil {
		copy(m.Flash, flash)
	}
	copy(m.Ports, ports)
	m.refreshMMUCache()

	cpuRaw, _ := json.Marshal(cpuMap)
	var cs CPUState
	if err := json.Unmarshal(cpuRaw, &cs); err != nil {
		return err
	}
	m.ApplyCPUState(cs)

	devRaw, err := json.Marshal(devs)
	if err != nil {
		return err
	}
	newDevices := &Devices{}
	if err := json.Unmarshal(devRaw, newDevices); err != nil {
		return errPlain("Incomplete snapshot")
	}
	m.Devices = newDevices

	logRaw, _ := json.Marshal(inputLog)
	var entries []InputLogEntry
	if err := json.Unmarshal(logRaw, &entries); err != nil {
		return errPlain("Incomplete snapshot")
	}
	if entries == nil {
		entries = []InputLogEntry{}
	}
	m.InputLog = entries

	m.Running = false
	m.Fault = nil
	m.Hit = nil
	m.Trace = []TraceRow{}
	m.StopReason = "snapshot restored"
	m.Event("snapshot.restored", Ordered())
	return nil
}

// Patch applies a verified equal-length ROM patch: the expected bytes must
// match at the offset, the replacement must be the same length and fit the
// image, and an optional inputSha256 must match the current ROM hash. The
// patch is applied to both the ROM image and live memory (flash on MTR2),
// survives resets, and is recorded with before/after hashes so it can be
// reverted LIFO — but only while the patched bytes are still exactly what
// this patch wrote. Firmware checksums are NOT recomputed; a patched image
// must carry consistent checksums in the patch set itself.
func (m *Machine) Patch(offsetV, expectedV, hexV, inputShaV any) (Patch, error) {
	var entry Patch
	offset, err := Number(offsetV, "offset", 0, int64(len(m.ROM)-1))
	if err != nil {
		return entry, err
	}
	before, err := Bytes(expectedV)
	if err != nil {
		return entry, err
	}
	after, err := Bytes(hexV)
	if err != nil {
		return entry, err
	}
	if len(before) != len(after) || int(offset)+len(after) > len(m.ROM) {
		return entry, errPlain("Patch must preserve image size")
	}
	if s, ok := inputShaV.(string); ok && s != "" && s != Hash(m.ROM) {
		return entry, errPlain("Input ROM hash mismatch")
	}
	if !bytes.Equal(m.ROM[offset:int(offset)+len(before)], before) {
		return entry, errPlain("Expected bytes do not match ROM")
	}
	entry = Patch{ID: len(m.Patches) + 1, Offset: int(offset),
		Before:      hex.EncodeToString(before),
		After:       hex.EncodeToString(after),
		InputSha256: Hash(m.ROM)}
	copy(m.ROM[offset:], after)
	target := m.Memory
	if m.Board.Family == "mtr2" {
		target = m.Flash
	}
	copy(target[offset:], after)
	entry.OutputSha256 = Hash(m.ROM)
	m.Patches = append(m.Patches, entry)
	raw, _ := json.Marshal(entry)
	m.Event("patch.applied", raw)
	return entry, nil
}

// RevertPatch undoes the most recent patch (LIFO), refusing if the patched
// bytes have changed since it was applied.
func (m *Machine) RevertPatch() (Patch, error) {
	var entry Patch
	if len(m.Patches) == 0 {
		return entry, errPlain("No patch to revert")
	}
	last := m.Patches[len(m.Patches)-1]
	afterBytes, err := hex.DecodeString(last.After)
	if err != nil || !bytes.Equal(m.ROM[last.Offset:last.Offset+len(afterBytes)], afterBytes) {
		return entry, errPlain("Patch bytes have changed")
	}
	beforeBytes, _ := hex.DecodeString(last.Before)
	copy(m.ROM[last.Offset:], beforeBytes)
	target := m.Memory
	if m.Board.Family == "mtr2" {
		target = m.Flash
	}
	copy(target[last.Offset:], beforeBytes)
	m.Patches = m.Patches[:len(m.Patches)-1]
	raw, _ := json.Marshal(last)
	m.Event("patch.reverted", raw)
	return last, nil
}
