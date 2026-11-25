package emu

// Z80 execution with the HD64180/Z180 instruction extensions (IN0/OUT0,
// TST/TSTIO, MLT, SLP and the OTIM/OTDM family). The core is instruction-
// accurate, not cycle-exact: each instruction returns an approximate T-state
// count which drives all peripheral timing through Machine.Tick.
//
// Conventions used throughout this file:
//   - idx selects the index register for DD/FD-prefixed instructions:
//     0 = none, 1 = IX, 2 = IY. Repeated prefixes keep the last one and
//     add 4 T-states each, matching the hardware's re-fetch behavior.
//   - addr is the precomputed (IX/IY+d) effective address, or -1 when the
//     instruction has no indexed memory operand. As on the real CPU, an
//     indexed instruction that also encodes (HL) uses the index displacement
//     for the memory access but plain H/L for register operands.
//   - Flag helpers preserve the undocumented Z80 behavior of copying result
//     bits 3 and 5 into flag bits 3 and 5 (see sz/szp); several instructions
//     overwrite those bits from other operands, exactly as the reference
//     implementation does.
//
// The R register increments only on true opcode fetches (bumpR), not on
// operand or prefixed-instruction continuation bytes.

const (
	FlagS = 0x80
	FlagZ = 0x40
	FlagH = 0x10
	FlagP = 0x04
	FlagN = 0x02
	FlagC = 0x01
)

func parity(n int) int {
	n ^= n >> 4
	n &= 15
	return (0x9669 >> n) & 1
}

func boolInt(cond bool, v int) int {
	if cond {
		return v
	}
	return 0
}

func sz(n int) int  { return (n & 0xa8) | boolInt(n == 0, FlagZ) }
func szp(n int) int { return sz(n) | boolInt(parity(n) != 0, FlagP) }

type CPU struct {
	A, F, B, C, D, E, H, L         uint8
	A2, F2, B2, C2, D2, E2, H2, L2 uint8
	IX, IY, SP, PC                 uint16
	I, R                           uint8
	IFF1, IFF2                     bool
	IM                             int
	Halted                         bool
	EIDelay                        int
	Cycles                         int64
	Instructions                   int64
}

func (c *CPU) Reset() {
	*c = CPU{}
}

func (c *CPU) idxReg(idx uint8) int {
	if idx == 1 {
		return int(c.IX)
	}
	return int(c.IY)
}

func (c *CPU) setIdxReg(idx uint8, v int) {
	if idx == 1 {
		c.IX = uint16(v & 65535)
	} else {
		c.IY = uint16(v & 65535)
	}
}

func (c *CPU) pair(p int, idx uint8) int {
	switch p {
	case 0:
		return int(c.B)<<8 | int(c.C)
	case 1:
		return int(c.D)<<8 | int(c.E)
	case 2:
		if idx != 0 {
			return c.idxReg(idx)
		}
		return int(c.H)<<8 | int(c.L)
	default:
		return int(c.SP)
	}
}

func (c *CPU) setPair(p, v int, idx uint8) {
	v &= 65535
	switch p {
	case 0:
		c.B, c.C = uint8(v>>8), uint8(v)
	case 1:
		c.D, c.E = uint8(v>>8), uint8(v)
	case 2:
		if idx != 0 {
			c.setIdxReg(idx, v)
		} else {
			c.H, c.L = uint8(v>>8), uint8(v)
		}
	default:
		c.SP = uint16(v)
	}
}

func (c *CPU) read(m *Machine, a int) int {
	return m.Read(a & 65535)
}

func (c *CPU) write(m *Machine, a, v int) {
	m.Write(a&65535, v&255)
}

func (c *CPU) word(m *Machine, a int) int {
	return c.read(m, a) | c.read(m, a+1)<<8
}

func (c *CPU) putWord(m *Machine, a, v int) {
	c.write(m, a, v)
	c.write(m, a+1, v>>8)
}

func (c *CPU) fetch(m *Machine) int {
	n := c.read(m, int(c.PC))
	c.PC = uint16((int(c.PC) + 1) & 65535)
	return n
}

// bumpR advances the R refresh counter exactly as an opcode fetch does.
func (c *CPU) bumpR() {
	c.R = uint8((int(c.R) & 128) | ((int(c.R) + 1) & 127))
}

// regSimple returns register n (0..7) with no index/memory handling.
func (c *CPU) regSimple(n int) int {
	switch n {
	case 0:
		return int(c.B)
	case 1:
		return int(c.C)
	case 2:
		return int(c.D)
	case 3:
		return int(c.E)
	case 4:
		return int(c.H)
	case 5:
		return int(c.L)
	default:
		return int(c.A)
	}
}

func (c *CPU) setRegSimple(n, v int) {
	v &= 255
	switch n {
	case 0:
		c.B = uint8(v)
	case 1:
		c.C = uint8(v)
	case 2:
		c.D = uint8(v)
	case 3:
		c.E = uint8(v)
	case 4:
		c.H = uint8(v)
	case 5:
		c.L = uint8(v)
	default:
		c.A = uint8(v)
	}
}

// reg reads operand register n (0..7); n==6 addresses memory at addr, or (HL) when addr is -1.
func (c *CPU) reg(m *Machine, n int, idx uint8, addr int) int {
	if n == 6 {
		a := addr
		if a < 0 {
			a = c.pair(2, 0)
		}
		return c.read(m, a)
	}
	if idx != 0 && (n == 4 || n == 5) {
		v := c.idxReg(idx)
		if n == 4 {
			return v >> 8
		}
		return v & 255
	}
	return c.regSimple(n)
}

func (c *CPU) setReg(m *Machine, n, v int, idx uint8, addr int) {
	v &= 255
	if n == 6 {
		a := addr
		if a < 0 {
			a = c.pair(2, 0)
		}
		c.write(m, a, v)
		return
	}
	if idx != 0 && (n == 4 || n == 5) {
		cur := c.idxReg(idx)
		if n == 4 {
			c.setIdxReg(idx, (v<<8)|(cur&255))
		} else {
			c.setIdxReg(idx, (cur&0xff00)|v)
		}
		return
	}
	c.setRegSimple(n, v)
}

func (c *CPU) cond(n int) bool {
	switch n {
	case 0:
		return c.F&FlagZ == 0
	case 1:
		return c.F&FlagZ != 0
	case 2:
		return c.F&FlagC == 0
	case 3:
		return c.F&FlagC != 0
	case 4:
		return c.F&FlagP == 0
	case 5:
		return c.F&FlagP != 0
	case 6:
		return c.F&FlagS == 0
	default:
		return c.F&FlagS != 0
	}
}

func (c *CPU) alu(op, v int) {
	a := int(c.A)
	carry := 0
	if op == 1 || op == 3 {
		carry = int(c.F & FlagC)
	}
	if op < 4 || op == 7 {
		sub := op == 2 || op == 3 || op == 7
		result := a + v + carry
		if sub {
			result = a - v - carry
		}
		r := result & 255
		f := sz(r) | boolInt((a^v^result)&16 != 0, FlagH) |
			boolInt(result < 0 || result > 255, FlagC)
		pv := (^(a ^ v)) & (a ^ r)
		if sub {
			pv = (a ^ v) & (a ^ r)
		}
		f |= boolInt(pv&128 != 0, FlagP)
		if sub {
			f |= FlagN
		}
		if op == 7 {
			c.F = uint8((f & 0xd7) | (v & 0x28))
		} else {
			c.F = uint8(f)
			c.A = uint8(r)
		}
		return
	}
	var result int
	switch op {
	case 4:
		result = a & v
	case 5:
		result = a ^ v
	default:
		result = a | v
	}
	c.A = uint8(result)
	f := szp(result)
	if op == 4 {
		f |= FlagH
	}
	c.F = uint8(f)
}

func (c *CPU) inc(v int, dec bool) int {
	r := v + 1
	if dec {
		r = v - 1
	}
	r &= 255
	f := (int(c.F) & FlagC) | sz(r) | boolInt((v^r)&16 != 0, FlagH)
	if dec {
		f |= FlagN
	}
	if (dec && v == 128) || (!dec && v == 127) {
		f |= FlagP
	}
	c.F = uint8(f)
	return r
}

func (c *CPU) add16(m *Machine, p, v int, idx uint8, sub, withCarry bool) {
	a := c.pair(p, idx)
	cy := 0
	if withCarry {
		cy = int(c.F & FlagC)
	}
	result := a + v + cy
	if sub {
		result = a - v - cy
	}
	r := result & 65535
	f := boolInt((a^v^result)&0x1000 != 0, FlagH) |
		boolInt(result < 0 || result > 65535, FlagC) |
		((r >> 8) & 0x28) | boolInt(sub, FlagN)
	if withCarry {
		pv := (^(a ^ v)) & (a ^ r)
		if sub {
			pv = (a ^ v) & (a ^ r)
		}
		f |= boolInt(r&0x8000 != 0, FlagS) | boolInt(r == 0, FlagZ) | boolInt(pv&0x8000 != 0, FlagP)
	} else {
		f |= int(c.F) & (FlagS | FlagZ | FlagP)
	}
	c.F = uint8(f)
	c.setPair(p, r, idx)
}

func (c *CPU) rotate(y, v int) int {
	var r, cc int
	switch y {
	case 0:
		cc = v >> 7
		r = (v << 1) | cc
	case 1:
		cc = v & 1
		r = (v >> 1) | (cc << 7)
	case 2:
		cc = v >> 7
		r = (v << 1) | int(c.F&FlagC)
	case 3:
		cc = v & 1
		r = (v >> 1) | (int(c.F&FlagC) << 7)
	case 4:
		cc = v >> 7
		r = v << 1
	case 5:
		cc = v & 1
		r = (v >> 1) | (v & 128)
	case 6:
		cc = v >> 7
		r = (v << 1) | 1
	default:
		cc = v & 1
		r = v >> 1
	}
	r &= 255
	c.F = uint8(szp(r) | cc)
	return r
}

func (c *CPU) cb(m *Machine, op int, idx uint8, addr int) int {
	x := op >> 6
	y := (op >> 3) & 7
	z := op & 7
	n := z
	if idx != 0 {
		n = 6
	}
	v := c.reg(m, n, 0, addr)
	r := v
	if x == 0 {
		r = c.rotate(y, v)
	}
	if x == 1 {
		f := (int(c.F) & FlagC) | FlagH
		if v&(1<<y) == 0 {
			f |= FlagZ | FlagP
		}
		if y == 7 && v&128 != 0 {
			f |= FlagS
		}
		if idx != 0 {
			f |= (addr >> 8) & 0x28
		} else {
			f |= v & 0x28
		}
		c.F = uint8(f)
	}
	if x == 2 {
		r = v & ^(1 << y)
	}
	if x == 3 {
		r = v | (1 << y)
	}
	if x != 1 {
		c.setReg(m, n, r, 0, addr)
		if idx != 0 && z != 6 {
			c.setRegSimple(z, r)
		}
	}
	if idx != 0 {
		return 23
	}
	if z == 6 {
		return 15
	}
	return 8
}

func (c *CPU) push(m *Machine, v int) {
	c.SP = uint16((int(c.SP) - 2) & 65535)
	c.putWord(m, int(c.SP), v)
}

func (c *CPU) pop(m *Machine) int {
	v := c.word(m, int(c.SP))
	c.SP = uint16((int(c.SP) + 2) & 65535)
	return v
}

func (c *CPU) nextWord(m *Machine) int {
	l := c.fetch(m)
	return l | c.fetch(m)<<8
}

func (c *CPU) signed(m *Machine) int {
	n := c.fetch(m)
	if n < 128 {
		return n
	}
	return n - 256
}

// Interrupt offers vector to the CPU (mode-2 offset, or RST 38h for external
// non-IM2 interrupts). Refused while masked or during the EI delay; returns
// whether it was taken.
func (c *CPU) Interrupt(m *Machine, vector int, external bool) (bool, error) {
	if !c.IFF1 || c.EIDelay != 0 {
		return false, nil
	}
	c.Halted = false
	c.IFF1, c.IFF2 = false, false
	c.push(m, int(c.PC))
	if external && c.IM != 2 {
		c.PC = 0x38
	} else {
		c.PC = uint16(c.word(m, (int(c.I)<<8)|vector))
	}
	c.Cycles += 13
	if err := m.Tick(13); err != nil {
		return true, err
	}
	return true, nil
}

// Step executes one instruction and returns its T-states.
func (c *CPU) Step(m *Machine) (int, error) {
	t := 4
	if !c.Halted {
		c.bumpR()
		op := c.fetch(m)
		var idx uint8
		prefix := 0
		for op == 0xdd || op == 0xfd {
			if op == 0xdd {
				idx = 1
			} else {
				idx = 2
			}
			prefix += 4
			if prefix > 65536 {
				return 0, errPlain("Unbounded index prefix sequence")
			}
			c.bumpR()
			op = c.fetch(m)
		}
		var err error
		if op == 0xed {
			c.bumpR()
			t, err = c.ed(m, c.fetch(m))
			t += prefix
		} else if op == 0xcb && idx != 0 {
			a := (c.idxReg(idx) + c.signed(m)) & 65535
			t = c.cb(m, c.fetch(m), idx, a) + prefix - 4
		} else {
			t, err = c.execute(m, op, idx)
			t += prefix
		}
		if err != nil {
			return t, err
		}
		c.Instructions++
	}
	if c.EIDelay != 0 {
		c.EIDelay--
	}
	c.Cycles += int64(t)
	if err := m.Tick(t); err != nil {
		return t, err
	}
	return t, nil
}
