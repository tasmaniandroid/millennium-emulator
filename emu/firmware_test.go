package emu

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	fixtureROM = flag.String("rom", "", "path to the TEST.BIN test and benchmark fixture")
	fixtureMap = flag.String("rom-map", "", "path to the symbol map (defaults to the same-stem .MAP beside the ROM)")
)

func readFixtureROM(t testing.TB) (string, []byte) {
	t.Helper()
	if *fixtureROM == "" {
		t.Skip("set -rom to run with the TEST.BIN fixture")
	}
	rom, err := os.ReadFile(*fixtureROM)
	if err != nil {
		t.Fatal(err)
	}
	return *fixtureROM, rom
}

// TestFirmwareAdvertisingScroll boots the real TEST.BIN image, injects a
// small routine that calls the firmware's own VFD write entry points (located
// through the companion symbol map), and asserts the display after the
// two-row advertising scroll sequence.
func TestFirmwareAdvertisingScroll(t *testing.T) {
	romPath, rom := readFixtureROM(t)
	mapPath := *fixtureMap
	if mapPath == "" {
		mapPath = strings.TrimSuffix(romPath, filepath.Ext(romPath)) + ".MAP"
	}
	mapRaw, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatal(err)
	}
	symbols := ParseMap(string(mapRaw))
	m := NewMachine("mtr1")
	if err := m.Load(rom, romPath, symbols, "mtr1"); err != nil {
		t.Fatal(err)
	}
	res := mustRun(t, m, 5000000)
	if res.Fault != nil {
		t.Fatalf("boot fault: %+v", res.Fault)
	}
	var dataSym, cmdSym *Symbol
	for i := range symbols {
		switch symbols[i].Name {
		case "_vfd_data_write":
			dataSym = &symbols[i]
		case "_vfd_cmd_write":
			cmdSym = &symbols[i]
		}
	}
	if dataSym == nil || cmdSym == nil {
		t.Fatalf("VFD write symbols not found in %s", mapPath)
	}
	var code []byte
	call := func(s *Symbol, value int) {
		code = append(code, 0x21, byte(value), 0, 0xe5, 0xcd,
			byte(s.Address&255), byte(s.Address>>8), 0xe1)
	}
	call(dataSym, 0x11)
	call(dataSym, 12)
	call(cmdSym, 0)
	for i := 0; i < len("TOP ROW IS UNCHANGED"+strings.Repeat(" ", 20)); i++ {
		call(dataSym, int(("TOP ROW IS UNCHANGED" + strings.Repeat(" ", 20))[i]))
	}
	call(dataSym, 0x15)
	call(cmdSym, 39)
	call(dataSym, 32)
	call(dataSym, 72)
	call(cmdSym, 39)
	call(dataSym, 72)
	call(dataSym, 73)
	stop := 0xfe00 + len(code)
	if stop >= 65535 {
		t.Fatal("code too long")
	}
	code = append(code, 0x76)
	phys := m.Physical(0xfe00)
	copy(m.Memory[phys:], code)
	m.CPU.PC = 0xfe00
	m.CPU.IFF1, m.CPU.IFF2 = false, false
	m.Breakpoints = []Breakpoint{{Space: "logical", Address: stop}}
	res2 := mustRun(t, m, 100000)
	if res2.Reason != "breakpoint" {
		t.Fatalf("reason=%s fault=%+v", res2.Reason, res2.Fault)
	}
	st := m.State()
	want := [2]string{"TOP ROW IS UNCHANGED", strings.Repeat(" ", 18) + "HI"}
	if st.Display != want {
		t.Fatalf("display=%q want %q", st.Display, want)
	}
}
