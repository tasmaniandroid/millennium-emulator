package emu

import "strconv"

// ed executes one ED-prefixed opcode. Beyond the standard Z80 block (IN/OUT
// via C, 16-bit ADC/SBC, LD rr,(nn), NEG, RETI/RETN, IM, I/R moves, RRD/RLD
// and the LDI/CPI/INI/OUTI families with their repeat forms), this covers
// the Z180 extensions: IN0/OUT0 (absolute-port I/O), TST r / TST n /
// TSTIO (C),n, MLT (8x8->16 multiply), SLP (halt-equivalent) and the
// OTIM/OTDM/OTIMR/OTDMR block-output instructions.
//
// Unsupported or illegal ED opcodes return an error rather than silently
// executing as NOPs; the run loop converts that into a machine fault.
// The returned error message embeds the opcode and the PC of the ED byte,
// both in unpadded lowercase hex; the exact wording is part of the API
// error contract.
func (c *CPU) ed(m *Machine, op int) (int, error) {
	y := (op >> 3) & 7
	p := y >> 1
	q := y & 1

	// Z180 IN0 r,(n) / IN0 (C-group form)
	if op < 0x40 && (op&7) == 0 {
		v := m.Input(c.fetch(m))
		if y != 6 {
			c.setRegSimple(y, v)
		}
		c.F = uint8((int(c.F) & FlagC) | szp(v))
		return 12, nil
	}
	// Z180 OUT0 (n),r
	if op < 0x40 && (op&7) == 1 {
		val := 0
		if y != 6 {
			val = c.regSimple(y)
		}
		m.Output(c.fetch(m), val)
		return 13, nil
	}
	// Z180 TST r
	if op < 0x40 && (op&7) == 4 {
		c.F = uint8(szp(int(c.A)&c.regSimple(y)) | FlagH)
		if y == 6 {
			return 10, nil
		}
		return 7, nil
	}
	// TST n
	if op == 0x64 {
		c.F = uint8(szp(int(c.A)&c.fetch(m)) | FlagH)
		return 9, nil
	}
	// TSTIO (C),n
	if op == 0x74 {
		c.F = uint8(szp(m.Input(int(c.C))&c.fetch(m)) | FlagH)
		return 12, nil
	}
	// MLT rr
	if op == 0x4c || op == 0x5c || op == 0x6c || op == 0x7c {
		v := c.pair(p, 0)
		c.setPair(p, (v>>8)*(v&255), 0)
		return 17, nil
	}
	// SLP
	if op == 0x76 {
		c.Halted = true
		return 8, nil
	}
	// OTIM/OTDM/OTIMR/OTDMR
	if op == 0x83 || op == 0x8b || op == 0x93 || op == 0x9b {
		dec := (op & 8) != 0
		v := c.read(m, c.pair(2, 0))
		m.Output(int(c.C), v)
		hl := c.pair(2, 0)
		if dec {
			hl--
		} else {
			hl++
		}
		c.setPair(2, hl, 0)
		cc := int(c.C)
		if dec {
			cc--
		} else {
			cc++
		}
		c.C = uint8(cc & 255)
		c.B = uint8((int(c.B) - 1) & 255)
		c.F = uint8(sz(int(c.B)) | FlagN)
		if (op&16) != 0 && c.B != 0 {
			c.PC = uint16((int(c.PC) - 2) & 65535)
			return 16, nil
		}
		return 14, nil
	}
	if (op & 0xc0) == 0x40 {
		switch op & 7 {
		case 0: // IN r,(C)
			v := m.Input(c.pair(0, 0))
			if y != 6 {
				c.setRegSimple(y, v)
			}
			c.F = uint8((int(c.F) & FlagC) | szp(v))
			return 12, nil
		case 1: // OUT (C),r
			val := 0
			if y != 6 {
				val = c.regSimple(y)
			}
			m.Output(c.pair(0, 0), val)
			return 12, nil
		case 2: // ADC/SBC HL,rr
			c.add16(m, 2, c.pair(p, 0), 0, q == 0, true)
			return 15, nil
		case 3: // LD rr,(nn) / LD (nn),rr
			a := c.nextWord(m)
			if q != 0 {
				c.setPair(p, c.word(m, a), 0)
			} else {
				c.putWord(m, a, c.pair(p, 0))
			}
			return 20, nil
		case 4: // NEG
			v := int(c.A)
			c.A = 0
			c.alu(2, v)
			return 8, nil
		case 5: // RETI/RETN
			c.PC = uint16(c.pop(m))
			c.IFF1 = c.IFF2
			return 14, nil
		case 6: // IM n
			c.IM = []int{0, 0, 1, 2, 0, 0, 1, 2}[y]
			return 8, nil
		case 7:
			switch y {
			case 0:
				c.I = c.A
				return 9, nil
			case 1:
				c.R = c.A
				return 9, nil
			case 2:
				c.A = c.I
				c.F = uint8((int(c.F) & FlagC) | sz(int(c.A)) | boolInt(c.IFF2, FlagP))
				return 9, nil
			case 3:
				c.A = c.R
				c.F = uint8((int(c.F) & FlagC) | sz(int(c.A)) | boolInt(c.IFF2, FlagP))
				return 9, nil
			case 4, 5: // RRD / RLD
				addr := c.pair(2, 0)
				v := c.read(m, addr)
				a := int(c.A)
				var nv int
				if y == 4 {
					nv = ((a & 15) << 4) | (v >> 4)
					c.A = uint8((a & 0xf0) | (v & 15))
				} else {
					nv = ((v << 4) | (a & 15))
					c.A = uint8((a & 0xf0) | (v >> 4))
				}
				c.write(m, addr, nv)
				c.F = uint8((int(c.F) & FlagC) | szp(int(c.A)))
				return 18, nil
			}
		}
	}
	if (op & 0xe4) == 0xa0 {
		z := op & 3
		dec := (op & 8) != 0
		repeat := (op & 16) != 0
		delta := 1
		if dec {
			delta = -1
		}
		again := false
		switch z {
		case 0: // LDI/LDD/LDIR/LDDR
			v := c.read(m, c.pair(2, 0))
			c.write(m, c.pair(1, 0), v)
			c.setPair(2, c.pair(2, 0)+delta, 0)
			c.setPair(1, c.pair(1, 0)+delta, 0)
			c.setPair(0, c.pair(0, 0)-1, 0)
			n := (int(c.A) + v) & 255
			f := int(c.F) & (FlagS | FlagZ | FlagC)
			f |= boolInt(c.pair(0, 0) != 0, FlagP)
			f |= n & 8
			f |= (n & 2) << 4
			c.F = uint8(f)
			again = c.pair(0, 0) != 0
		case 1: // CPI/CPD/CPIR/CPDR
			v := c.read(m, c.pair(2, 0))
			r := (int(c.A) - v) & 255
			h := (int(c.A) ^ v ^ r) & 16
			n := (r - boolInt(h != 0, 1)) & 255
			c.setPair(2, c.pair(2, 0)+delta, 0)
			c.setPair(0, c.pair(0, 0)-1, 0)
			f := int(c.F) & FlagC
			f |= sz(r) & 0xd7
			f |= FlagN
			if h != 0 {
				f |= FlagH
			}
			f |= boolInt(c.pair(0, 0) != 0, FlagP)
			f |= n & 8
			f |= (n & 2) << 4
			c.F = uint8(f)
			again = c.pair(0, 0) != 0 && r != 0
		case 2, 3: // INI/IND/INIR/INDR and OUTI/OUTD/OTIR/OTDR
			addr := c.pair(2, 0)
			port := c.pair(0, 0)
			var v int
			if z == 2 {
				v = m.Input(port)
				c.write(m, addr, v)
			} else {
				v = c.read(m, addr)
			}
			c.B = uint8((int(c.B) - 1) & 255)
			if z == 3 {
				m.Output(c.pair(0, 0), v)
			}
			c.setPair(2, addr+delta, 0)
			k := v + int(c.L)
			if z == 2 {
				k = v + ((int(c.C) + delta) & 255)
			}
			f := sz(int(c.B))
			if v&128 != 0 {
				f |= FlagN
			}
			if k > 255 {
				f |= FlagH | FlagC
			}
			if parity((k&7)^int(c.B)) != 0 {
				f |= FlagP
			}
			c.F = uint8(f)
			again = c.B != 0
		}
		if repeat && again {
			c.PC = uint16((int(c.PC) - 2) & 65535)
			return 21, nil
		}
		return 16, nil
	}
	return 0, errPlain("Unsupported/illegal Z180 ED opcode " + strconv.FormatInt(int64(op), 16) +
		" at " + strconv.FormatInt(int64((int(c.PC)-2)&65535), 16))
}
