package emu

import (
	"strings"
	"testing"
)

type ioRec struct{ port, value int }

// cpuWith builds a flat-memory mtr1 machine with identity MMU for CPU tests,
// i.e. a flat 64 KiB read/write bus with input(p)=p&255 and recorded outputs.
func cpuWith(code []byte) (*Machine, *[]ioRec) {
	m := NewMachine("mtr1")
	for i := range m.Memory {
		m.Memory[i] = 0
	}
	copy(m.Memory, code)
	io := &[]ioRec{}
	m.InputOverride = func(p int) int { return p & 255 }
	m.OutputOverride = func(p, v int) { *io = append(*io, ioRec{p, v}) }
	m.ReadOverride = func(a int) int { return int(m.Memory[a&65535]) }
	m.WriteOverride = func(a, v int) { m.Memory[a&65535] = byte(v) }
	return m, io
}

func signed(n int) int {
	if n > 127 {
		return n - 256
	}
	return n
}

func TestExhaustiveALUFlags(t *testing.T) {
	m, _ := cpuWith(nil)
	c := &m.CPU
	for _, op := range []int{0, 1, 2, 3, 7} {
		for a := 0; a < 256; a++ {
			for b := 0; b < 256; b++ {
				carries := []int{0}
				if op == 1 || op == 3 {
					carries = []int{0, 1}
				}
				for _, carry := range carries {
					c.A = uint8(a)
					c.F = uint8(carry)
					c.alu(op, b)
					sub := op == 2 || op == 3 || op == 7
					full := a + b + carry
					if sub {
						full = a - b - carry
					}
					r := full & 255
					math := signed(a) + signed(b) + carry
					if sub {
						math = signed(a) - signed(b) - carry
					}
					var half bool
					if sub {
						half = (a&15)-(b&15)-carry < 0
					} else {
						half = (a&15)+(b&15)+carry > 15
					}
					expected := 0
					if r&128 != 0 {
						expected |= FlagS
					}
					if r == 0 {
						expected |= FlagZ
					}
					if half {
						expected |= FlagH
					}
					if math < -128 || math > 127 {
						expected |= FlagP
					}
					if sub {
						expected |= FlagN
					}
					if full < 0 || full > 255 {
						expected |= FlagC
					}
					if int(c.F)&0xd7 != expected {
						t.Fatalf("flags op=%d a=%d b=%d carry=%d: got %02x want %02x", op, a, b, carry, c.F, expected)
					}
					want := r
					if op == 7 {
						want = a
					}
					if int(c.A) != want {
						t.Fatalf("A op=%d a=%d b=%d: got %d want %d", op, a, b, c.A, want)
					}
				}
			}
		}
	}
}

func TestIndexedMemoryTransfersUseRealHL(t *testing.T) {
	m, _ := cpuWith([]byte{0xdd, 0x21, 0x00, 0x20, 0x26, 0x5a, 0xdd, 0x74, 0xff, 0xdd, 0x66, 0xfe})
	m.Memory[0x1ffe] = 0xa5
	c := &m.CPU
	for i := 0; i < 4; i++ {
		if _, err := c.Step(m); err != nil {
			t.Fatal(err)
		}
	}
	if m.Memory[0x1fff] != 0x5a {
		t.Fatalf("memory[0x1fff]=%02x", m.Memory[0x1fff])
	}
	if c.H != 0xa5 {
		t.Fatalf("H=%02x", c.H)
	}
	if c.IX != 0x2000 {
		t.Fatalf("IX=%04x", c.IX)
	}
}

func TestIndexedCBRotateCopiesBoth(t *testing.T) {
	m, _ := cpuWith([]byte{0xdd, 0xcb, 0xff, 0x00})
	m.CPU.IX = 0x2001
	m.Memory[0x2000] = 0x81
	if _, err := m.CPU.Step(m); err != nil {
		t.Fatal(err)
	}
	if m.CPU.B != 3 {
		t.Fatalf("B=%d", m.CPU.B)
	}
	if m.Memory[0x2000] != 3 {
		t.Fatalf("mem=%d", m.Memory[0x2000])
	}
	if m.CPU.F&FlagC != 1 {
		t.Fatalf("F=%02x", m.CPU.F)
	}
}

func TestLDIRAcrossBoundaries(t *testing.T) {
	m, _ := cpuWith([]byte{0xed, 0xb0})
	c := &m.CPU
	c.setPair(0, 3, 0)
	c.setPair(1, 0x2000, 0)
	c.setPair(2, 0x1000, 0)
	copy(m.Memory[0x1000:], []byte{0x12, 0x34, 0x56})
	if _, err := c.Step(m); err != nil {
		t.Fatal(err)
	}
	if c.PC != 0 {
		t.Fatalf("pc=%d after first", c.PC)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.Step(m); err != nil {
			t.Fatal(err)
		}
	}
	if c.PC != 2 {
		t.Fatalf("pc=%d", c.PC)
	}
	if c.pair(0, 0) != 0 {
		t.Fatalf("BC=%d", c.pair(0, 0))
	}
	if m.Memory[0x2000] != 0x12 || m.Memory[0x2001] != 0x34 || m.Memory[0x2002] != 0x56 {
		t.Fatalf("dest=%02x %02x %02x", m.Memory[0x2000], m.Memory[0x2001], m.Memory[0x2002])
	}
}

func TestZ180MLTIN0OUT0TSTSLP(t *testing.T) {
	m, io := cpuWith([]byte{0x01, 0x07, 0x09, 0xed, 0x4c, 0xed, 0x38, 0x41, 0xed, 0x39, 0x42, 0xed, 0x64, 0x40, 0xed, 0x76})
	c := &m.CPU
	for i := 0; i < 6; i++ {
		if _, err := c.Step(m); err != nil {
			t.Fatal(err)
		}
	}
	if c.pair(0, 0) != 63 {
		t.Fatalf("BC=%d", c.pair(0, 0))
	}
	if c.A != 0x41 {
		t.Fatalf("A=%02x", c.A)
	}
	if len(*io) != 1 || (*io)[0] != (ioRec{0x42, 0x41}) {
		t.Fatalf("io=%v", *io)
	}
	if c.F&FlagZ != 0 {
		t.Fatalf("F=%02x", c.F)
	}
	if !c.Halted {
		t.Fatal("not halted")
	}
}

func TestEIDelaysOneInstruction(t *testing.T) {
	m, _ := cpuWith([]byte{0xfb, 0x00, 0x76})
	c := &m.CPU
	c.SP = 0xf000
	c.I = 1
	m.Memory[0x104] = 0x34
	m.Memory[0x105] = 0x12
	if _, err := c.Step(m); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Interrupt(m, 4, false); ok {
		t.Fatal("interrupt accepted too early")
	}
	if _, err := c.Step(m); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Interrupt(m, 4, false); !ok {
		t.Fatal("interrupt not accepted")
	}
	if c.PC != 0x1234 {
		t.Fatalf("pc=%04x", c.PC)
	}
	if v := c.pop(m); v != 2 {
		t.Fatalf("popped %d", v)
	}
	if c.IFF1 {
		t.Fatal("iff1 still set")
	}
}

func TestDAABCD(t *testing.T) {
	m, _ := cpuWith(nil)
	c := &m.CPU
	cases := [][5]int{{0x45, 0x38, 0, 0x83, 0}, {0x99, 1, 0, 0, 1}, {0x10, 1, 2, 9, 0}, {0, 1, 2, 0x99, 1}}
	for _, tc := range cases {
		c.A = uint8(tc[0])
		c.F = 0
		c.alu(tc[2], tc[1])
		if _, err := c.execute(m, 0x27, 0); err != nil {
			t.Fatal(err)
		}
		if int(c.A) != tc[3] {
			t.Fatalf("a=%02x want %02x", c.A, tc[3])
		}
		if int(c.F&FlagC) != tc[4] {
			t.Fatalf("carry=%d want %d (a=%02x)", c.F&FlagC, tc[4], c.A)
		}
	}
}

func TestIllegalEDTraps(t *testing.T) {
	m, _ := cpuWith([]byte{0xed, 0xff})
	_, err := m.CPU.Step(m)
	if err == nil || !strings.Contains(err.Error(), "illegal") {
		t.Fatalf("err=%v", err)
	}
}

func TestMMUTranslationAndROMReadOnly(t *testing.T) {
	m := NewMachine("mtr1")
	if err := m.Load([]byte{0x00}, "firmware.bin", nil, "auto"); err != nil {
		t.Fatal(err)
	}
	_ = m.Output(0x3a, 0x85)
	_ = m.Output(0x38, 0x38)
	_ = m.Output(0x39, 0x12)
	if got := m.Physical(0x4000); got != 0x4000 {
		t.Fatalf("phys(0x4000)=%x", got)
	}
	if got := m.Physical(0x5000); got != 0x17000 {
		t.Fatalf("phys(0x5000)=%x", got)
	}
	if got := m.Physical(0x8000); got != 0x40000 {
		t.Fatalf("phys(0x8000)=%x", got)
	}
	m.Write(0x8000, 0x42)
	if m.Memory[0x40000] != 0x42 {
		t.Fatalf("ram=%02x", m.Memory[0x40000])
	}
	m.Write(0, 0xff)
	if m.Memory[0] != 0 {
		t.Fatalf("rom writable: %02x", m.Memory[0])
	}
	_ = m.Output(0x38, 255)
	if got := m.Physical(0xffff); got != 0xefff {
		t.Fatalf("phys(0xffff)=%x", got)
	}
}

func TestParseMapOrdering(t *testing.T) {
	rows := ParseMap("00047960  0000F960  _back_vfd_state  BSS  (XINIT)\n00028000  00005000  _example BCODE (TEST)")
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].Physical != 0x47960 || rows[0].Address != 0xf960 {
		t.Fatalf("row0=%+v", rows[0])
	}
	if rows[1].Physical != 0x28000 {
		t.Fatalf("row1=%+v", rows[1])
	}
}
