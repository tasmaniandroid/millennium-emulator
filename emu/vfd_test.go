package emu

import (
	"strings"
	"testing"
)

type phoneHelper struct {
	m       *Machine
	data    func(v int)
	command func(v int)
	text    func(s string)
	lines   func() [2]string
}

func phone() *phoneHelper {
	m := NewMachine("mtr1")
	if err := m.Load([]byte{0, 0xc3, 0, 0}, "firmware.bin", nil, "auto"); err != nil {
		panic(err)
	}
	p := &phoneHelper{m: m}
	p.data = func(v int) { _ = m.Output(0x41, 0x10); _ = m.Output(0xa0, v) }
	p.command = func(v int) { _ = m.Output(0x41, 0x30); _ = m.Output(0xa0, v) }
	p.text = func(s string) {
		for i := 0; i < len(s); i++ {
			p.data(int(s[i]))
		}
	}
	p.lines = func() [2]string { return m.State().Display }
	return p
}

func eqLines(t *testing.T, got [2]string, want [2]string, ctx string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: lines=%q want %q", ctx, got, want)
	}
}

func TestCoinBasicBoardVFDPort(t *testing.T) {
	m := NewMachine("mtr1")
	if err := m.Load([]byte{0}, "coin-basic.bin", nil, "mtr1-coin-basic"); err != nil {
		t.Fatal(err)
	}
	_ = m.Output(0x41, 0x0f)
	_ = m.Output(0xc0, 0x41)
	if !strings.HasPrefix(m.State().Display[0], "A") {
		t.Fatalf("display=%q", m.State().Display[0])
	}
	if len(m.Devices.Voice) != 0 {
		t.Fatal("voice not empty")
	}
	_ = m.Output(0xa0, 0x42)
	if strings.TrimSpace(m.State().Display[0]) != "A" {
		t.Fatalf("display=%q", m.State().Display[0])
	}
	if m.SerialCycles(0) != 102400 {
		t.Fatalf("serialCycles=%d", m.SerialCycles(0))
	}
	_ = m.Output(0x06, 0x67)
	if len(m.Devices.AsciRx[0]) != 1 || m.Devices.AsciRx[0][0] != 0x41 {
		t.Fatalf("asciRx=%v", m.Devices.AsciRx[0])
	}
}

func TestNormalAddressingWraps(t *testing.T) {
	p := phone()
	p.text("ABCDEFGHIJKLMNOPQRSTabcdefghijklmnopqrst")
	eqLines(t, p.lines(), [2]string{"ABCDEFGHIJKLMNOPQRST", "abcdefghijklmnopqrst"}, "fill")
	if p.m.Devices.Cursor != 0 {
		t.Fatalf("cursor=%d", p.m.Devices.Cursor)
	}
	p.text("X")
	eqLines(t, p.lines(), [2]string{"XBCDEFGHIJKLMNOPQRST", "abcdefghijklmnopqrst"}, "wrap")
}

func TestHorizontalOverflowStaysOnRow(t *testing.T) {
	p := phone()
	p.text(strings.Repeat(" ", 20) + "BOTTOM LINE STAYS OK")
	p.data(0x15)
	p.command(0)
	p.text("ABCDEFGHIJKLMNOPQRST")
	eqLines(t, p.lines(), [2]string{"ABCDEFGHIJKLMNOPQRST", "BOTTOM LINE STAYS OK"}, "row")
	if p.m.Devices.Cursor != 19 || !p.m.Devices.DisplayScrollPending {
		t.Fatalf("cursor=%d pending=%v", p.m.Devices.Cursor, p.m.Devices.DisplayScrollPending)
	}
	p.text("UV")
	eqLines(t, p.lines(), [2]string{"CDEFGHIJKLMNOPQRSTUV", "BOTTOM LINE STAYS OK"}, "scroll")
	if p.m.Devices.Cursor != 19 {
		t.Fatalf("cursor=%d", p.m.Devices.Cursor)
	}
}

func TestAdvertisingPairsScrollOnce(t *testing.T) {
	p := phone()
	heading := "TOP ROW IS UNCHANGED"
	p.text(heading + strings.Repeat(" ", 20))
	p.data(0x15)
	message := "ABCDEFGHIJKLMNOPQRSTuvwxyz1234567890abcd"
	previous := " "
	for i := 0; i < len(message); i++ {
		p.command(39)
		p.text(previous)
		p.text(message[i : i+1])
		previous = message[i : i+1]
		want := strings.Repeat(" ", 20) + message[:i+1]
		if len(want) > 20 {
			want = want[len(want)-20:]
		}
		eqLines(t, p.lines(), [2]string{heading, want}, "step")
	}
	for i := 0; i < 20; i++ {
		p.command(39)
		p.text(previous + " ")
		previous = " "
	}
	eqLines(t, p.lines(), [2]string{heading, strings.Repeat(" ", 20)}, "clear-out")
}

func TestMarginOverwriteThenScroll(t *testing.T) {
	p := phone()
	p.text("TOP ROW IS UNCHANGED" + "ABCDEFGHIJKLMNOPQRST")
	p.data(0x15)
	p.command(39)
	p.text("Z")
	if p.lines()[1] != "ABCDEFGHIJKLMNOPQRSZ" {
		t.Fatalf("line2=%q", p.lines()[1])
	}
	p.command(39)
	p.text("Y")
	if p.lines()[1] != "ABCDEFGHIJKLMNOPQRSY" {
		t.Fatalf("line2=%q", p.lines()[1])
	}
	p.text("X")
	if p.lines()[1] != "BCDEFGHIJKLMNOPQRSYX" {
		t.Fatalf("line2=%q", p.lines()[1])
	}
	p.command(30)
	p.text("K")
	if p.m.Devices.Cursor != 31 || p.m.Devices.DisplayScrollPending {
		t.Fatalf("cursor=%d pending=%v", p.m.Devices.Cursor, p.m.Devices.DisplayScrollPending)
	}
}
