package emu

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func decodeB64(s string) []byte {
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return out
}

func encodeB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func loaded() *Machine {
	m := NewMachine("mtr1")
	if err := m.Load([]byte{0x3e, 0, 0x3c, 0xc3, 0x02, 0x00}, "firmware.bin", nil, "auto"); err != nil {
		panic(err)
	}
	return m
}

func mustRun(t *testing.T, m *Machine, n int64) RunResult {
	t.Helper()
	r, err := m.Run(RunOptions{Instructions: n, Cycles: int64(^uint64(0) >> 1)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPatchValidationAndRevert(t *testing.T) {
	m := loaded()
	original := append([]byte(nil), m.ROM...)
	if _, err := m.Patch(float64(0), "00", "ff", nil); err == nil || err.Error() != "Expected bytes do not match ROM" {
		t.Fatalf("expected bytes err=%v", err)
	}
	if _, err := m.Patch(float64(0), "3e", "ff", "wrong"); err == nil || err.Error() != "Input ROM hash mismatch" {
		t.Fatalf("hash err=%v", err)
	}
	if _, err := m.Patch(float64(0), "3e", "ffff", nil); err == nil || err.Error() != "Patch must preserve image size" {
		t.Fatalf("size err=%v", err)
	}
	p, err := m.Patch(float64(0), "3e", "00", Hash(m.ROM))
	if err != nil {
		t.Fatal(err)
	}
	if m.Memory[0] != 0 {
		t.Fatalf("memory[0]=%d", m.Memory[0])
	}
	if p.OutputSha256 != Hash(m.ROM) {
		t.Fatal("outputSha256 mismatch")
	}
	if Hash(m.Original) != Hash(original) {
		t.Fatal("original mutated")
	}
	m.Reset(false)
	if m.Memory[0] != 0 {
		t.Fatal("patch did not persist across reset")
	}
	if _, err := m.RevertPatch(); err != nil {
		t.Fatal(err)
	}
	if Hash(m.ROM) != Hash(original) {
		t.Fatal("rom not reverted")
	}
	if m.Memory[0] != 0x3e {
		t.Fatalf("memory[0]=%02x after revert", m.Memory[0])
	}
}

func TestSnapshotReproducesExecution(t *testing.T) {
	m := loaded()
	m.Devices.Eeprom[0] = 0x42
	if err := m.DeviceEvent("key", NewEventData("key", "1"), true); err != nil {
		t.Fatal(err)
	}
	mustRun(t, m, 1000)
	snapshot, _ := m.Snapshot()
	mustRun(t, m, 2000)
	after, _ := m.Snapshot()
	if err := m.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	mustRun(t, m, 2000)
	replay, _ := m.Snapshot()
	if string(replay) != string(after) {
		t.Fatalf("snapshot replay diverged:\n%s\n%s", replay, after)
	}
	if m.Devices.Eeprom[0] != 0x42 {
		t.Fatal("eeprom lost")
	}
	if _, err := m.Patch(float64(0), "3e", "00", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(snapshot); err == nil || err.Error() != "Snapshot does not match loaded ROM or board" {
		t.Fatalf("expected match error, got %v", err)
	}
}

func TestWarmColdReset(t *testing.T) {
	m := loaded()
	m.Memory[0x40000] = 123
	m.Devices.Eeprom[5] = 42
	m.Reset(false)
	if m.Memory[0x40000] != 123 || m.Devices.Eeprom[5] != 42 {
		t.Fatal("warm reset lost NVRAM")
	}
	m.Reset(true)
	if m.Memory[0x40000] != 0 || m.Devices.Eeprom[5] != 255 {
		t.Fatal("cold reset kept NVRAM")
	}
}

func TestBreakpointWatchpoint(t *testing.T) {
	m := NewMachine("mtr1")
	if err := m.Load([]byte{0x3e, 0x5a, 0x32, 0, 0x80, 0}, "firmware.bin", nil, "auto"); err != nil {
		t.Fatal(err)
	}
	_ = m.Output(0x38, 0x38)
	_ = m.Output(0x3a, 0x80)
	m.Breakpoints = []Breakpoint{{Space: "physical", Address: 2}}
	first := mustRun(t, m, 10)
	if first.Reason != "breakpoint" || m.CPU.PC != 2 {
		t.Fatalf("reason=%s pc=%d", first.Reason, m.CPU.PC)
	}
	m.Watchpoints = []Watchpoint{{Kind: "write", Space: "physical", Address: 0x40000, Length: 1}}
	second, _ := m.Run(RunOptions{Instructions: 1, Cycles: int64(^uint64(0) >> 1), IgnoreBreakpoints: true})
	if second.Reason != "watchpoint" {
		t.Fatalf("reason=%s", second.Reason)
	}
	if m.Memory[0x40000] != 0x5a {
		t.Fatalf("memory=%02x", m.Memory[0x40000])
	}
}

func TestTelephonyChecksums(t *testing.T) {
	m := loaded()
	for _, cmd := range []int{0x31, 0x34, 0x38} {
		if err := m.TelephonyByte(cmd); err != nil {
			t.Fatal(err)
		}
		q := m.Devices.IpQueue
		msg := q[len(q)-1]
		m.Devices.IpQueue = q[:len(q)-1]
		if int(msg[1]) != len(msg) {
			t.Fatalf("cmd %02x length %d != %d", cmd, msg[1], len(msg))
		}
		sum := 0
		for _, b := range msg[:len(msg)-1] {
			sum = (sum + int(b)) & 255
		}
		if int(msg[len(msg)-1]) != sum {
			t.Fatalf("cmd %02x checksum", cmd)
		}
	}
}

func TestMicrowireEEPROM(t *testing.T) {
	m := loaded()
	select_ := func() { _ = m.Output(0x60, 0x40) }
	deselect := func() { _ = m.Output(0x60, 0) }
	bit := func(v int) {
		_ = m.Output(0x40, v)
		_ = m.Output(0x41, 0)
		_ = m.Output(0x41, 1)
		_ = m.Output(0x41, 0)
	}
	command := func(v int) {
		select_()
		for i := 8; i >= 0; i-- {
			bit((v >> i) & 1)
		}
	}
	command(0x130) // EWEN: start + 00 + 11xxxx
	deselect()
	command(0x145) // WRITE word 5
	for i := 15; i >= 0; i-- {
		bit((0xa55a >> i) & 1)
	}
	deselect()
	if m.Devices.Eeprom[10] != 0xa5 || m.Devices.Eeprom[11] != 0x5a {
		t.Fatalf("eeprom=%02x %02x", m.Devices.Eeprom[10], m.Devices.Eeprom[11])
	}
	command(0x185) // READ word 5
	word := 0
	if v := (m.Input(0x80) >> 3) & 1; v != 0 {
		t.Fatalf("leading bit=%d", v)
	}
	bit(0)
	for i := 0; i < 16; i++ {
		word = (word << 1) | ((m.Input(0x80) >> 3) & 1)
		bit(0)
	}
	deselect()
	if word != 0xa55a {
		t.Fatalf("read word=%04x", word)
	}
}

func TestSensorAndVFDWrites(t *testing.T) {
	m := loaded()
	if v := m.Input(0x80) & 0xf0; v != 0xf0 {
		t.Fatalf("status=%02x", v)
	}
	if err := m.DeviceEvent("sensor", NewEventData("coverOpen", true), true); err != nil {
		t.Fatal(err)
	}
	if v := m.Input(0x80) & 32; v != 0 {
		t.Fatalf("cover bit=%02x", v)
	}
	_ = m.Output(0x41, 0x10)
	_ = m.Output(0xa0, 65)
	_ = m.Output(0xa0, 66)
	st := m.State()
	if st.Display[0][:2] != "AB" {
		t.Fatalf("display=%v", st.Display)
	}
	_ = m.Output(0x41, 0x30)
	_ = m.Output(0xa0, 20)
	_ = m.Output(0x41, 0x10)
	_ = m.Output(0xa0, 67)
	st = m.State()
	if st.Display[1][0] != 'C' {
		t.Fatalf("line2=%q", st.Display[1])
	}
}

func TestCoinValidatorCommands(t *testing.T) {
	m := loaded()
	m.CoinByte(122)
	if err := m.DeviceEvent("coin", NewEventData("index", float64(0)), true); err != nil {
		t.Fatal(err)
	}
	if len(m.Devices.CoinReturned) != 1 {
		t.Fatalf("returned=%v", m.Devices.CoinReturned)
	}
	m.CoinByte(97)
	if err := m.DeviceEvent("coin", NewEventData("index", float64(1)), true); err != nil {
		t.Fatal(err)
	}
	if len(m.Devices.CoinEscrow) != 1 || m.Devices.CoinEscrow[0] != 1 {
		t.Fatalf("escrow=%v", m.Devices.CoinEscrow)
	}
	m.CoinByte(99)
	if len(m.Devices.CoinCollected) != 1 || m.Devices.CoinCollected[0] != 1 || len(m.Devices.CoinEscrow) != 0 {
		t.Fatalf("collected=%v escrow=%v", m.Devices.CoinCollected, m.Devices.CoinEscrow)
	}
	m.CoinByte(114)
	rx := m.Devices.AsciRx[0]
	tail := rx[len(rx)-3:]
	if tail[0] != 66 || tail[1] != 0 || tail[2] != 0 {
		t.Fatalf("asciRx tail=%v", tail)
	}
}

func TestSameCycleReplayOrder(t *testing.T) {
	m := loaded()
	baseline, _ := m.Snapshot()
	if err := m.DeviceEvent("key", NewEventData("key", "1"), true); err != nil {
		t.Fatal(err)
	}
	if err := m.DeviceEvent("key", NewEventData("key", "2"), true); err != nil {
		t.Fatal(err)
	}
	inputs := append([]InputLogEntry(nil), m.InputLog...)
	mustRun(t, m, 100)
	after, _ := m.Snapshot()
	if err := m.Restore(baseline); err != nil {
		t.Fatal(err)
	}
	for _, e := range inputs {
		m.Devices.Scheduled = append(m.Devices.Scheduled,
			ScheduledEvent{Cycle: e.Cycle, Type: e.Type, Data: e.Data.Clone()})
	}
	mustRun(t, m, 100)
	replay, _ := m.Snapshot()
	var a, b map[string]json.RawMessage
	_ = json.Unmarshal(after, &a)
	_ = json.Unmarshal(replay, &b)
	if string(a["cpu"]) != string(b["cpu"]) {
		t.Fatalf("cpu differs:\n%s\n%s", a["cpu"], b["cpu"])
	}
	if string(a["devices"]) != string(b["devices"]) {
		t.Fatalf("devices differs:\n%s\n%s", a["devices"], b["devices"])
	}
}

func TestMalformedSnapshotsRejectedAtomically(t *testing.T) {
	m := loaded()
	before, _ := m.Snapshot()
	var bad map[string]any
	_ = json.Unmarshal(before, &bad)
	bad["devices"].(map[string]any)["display"] = "bad"
	badRaw, _ := json.Marshal(bad)
	if err := m.Restore(badRaw); err == nil {
		t.Fatal("bad display accepted")
	}
	afterFail, _ := m.Snapshot()
	if string(afterFail) != string(before) {
		t.Fatal("machine mutated by failed restore")
	}
	var wrong map[string]any
	_ = json.Unmarshal(before, &wrong)
	mem := string(wrong["memory"].(string))
	// corrupt the first base64 char group: decode, flip a byte, re-encode
	raw := decodeB64(mem)
	raw[0] = 0
	wrong["memory"] = encodeB64(raw)
	wrongRaw, _ := json.Marshal(wrong)
	err := m.Restore(wrongRaw)
	if err == nil || err.Error() != "Snapshot ROM bytes do not match loaded ROM" {
		t.Fatalf("err=%v", err)
	}
	afterFail2, _ := m.Snapshot()
	if string(afterFail2) != string(before) {
		t.Fatal("machine mutated by failed ROM restore")
	}
}
