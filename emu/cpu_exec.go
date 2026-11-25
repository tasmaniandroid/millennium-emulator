package emu

import "strconv"

// execute runs one non-ED opcode, optionally DD/FD-indexed, and returns its
// T-state count. The decode follows the classic x/y/z octet decomposition of
// the Z80 opcode map (x = op>>6, y = (op>>3)&7, z = op&7, p = y>>1, q = y&1):
//
//	x=0: misc (NOP, EX AF/AF', DJNZ/JR, LD rr/ADD HL, LD via (rr)/(nn),
//	     INC/DEC rr, INC/DEC r, LD r,n, accumulator rotates, DAA/CPL/SCF/CCF)
//	x=1: LD r,r' (and HALT at 0x76)
//	x=2: ALU A,r
//	x=3: block/stack/jump/call/RST, CB prefix, and I/O immediate forms
//
// IN A,(n) / OUT (n),A deliberately place the current A register in the HIGH
// byte of the 16-bit port address, as the hardware does; the board decode
// uses the low byte.
func (c *CPU) execute(m *Machine, op int, idx uint8) (int, error) {
	x := op >> 6
	y := (op >> 3) & 7
	z := op & 7
	p := y >> 1
	q := y & 1
	addr := -1

	memOperand := idx != 0 && ((x == 0 && (z == 4 || z == 5 || z == 6) && y == 6) ||
		(x == 1 && op != 0x76 && (y == 6 || z == 6)) ||
		(x == 2 && z == 6))
	if memOperand {
		addr = (c.idxReg(idx) + c.signed(m)) & 65535
	}

	if x == 0 {
		switch z {
		case 0:
			if y == 0 {
				return 4, nil // NOP
			}
			if y == 1 { // EX AF,AF'
				c.A, c.A2 = c.A2, c.A
				c.F, c.F2 = c.F2, c.F
				return 4, nil
			}
			d := c.signed(m)
			if y == 2 { // DJNZ
				c.B = uint8((int(c.B) - 1) & 255)
				if c.B != 0 {
					c.PC = uint16((int(c.PC) + d) & 65535)
					return 13, nil
				}
				return 8, nil
			}
			if y == 3 || c.cond(y-4) { // JR / JR cc
				c.PC = uint16((int(c.PC) + d) & 65535)
				return 12, nil
			}
			return 7, nil
		case 1:
			if q == 0 {
				c.setPair(p, c.nextWord(m), idx)
				return 10, nil
			}
			c.add16(m, 2, c.pair(p, idx), idx, false, false)
			return 11, nil
		case 2:
			if p < 2 {
				if q != 0 {
					c.A = uint8(c.read(m, c.pair(p, 0)))
				} else {
					c.write(m, c.pair(p, 0), int(c.A))
				}
				return 7, nil
			}
			a := c.nextWord(m)
			if p == 2 {
				if q != 0 {
					c.setPair(2, c.word(m, a), idx)
				} else {
					c.putWord(m, a, c.pair(2, idx))
				}
				return 16, nil
			}
			if q != 0 {
				c.A = uint8(c.read(m, a))
			} else {
				c.write(m, a, int(c.A))
			}
			return 13, nil
		case 3:
			v := c.pair(p, idx)
			if q != 0 {
				v--
			} else {
				v++
			}
			c.setPair(p, v, idx)
			return 6, nil
		case 4:
			c.setReg(m, y, c.inc(c.reg(m, y, idx, addr), false), idx, addr)
			if y == 6 {
				if idx != 0 {
					return 19, nil
				}
				return 11, nil
			}
			return 4, nil
		case 5:
			c.setReg(m, y, c.inc(c.reg(m, y, idx, addr), true), idx, addr)
			if y == 6 {
				if idx != 0 {
					return 19, nil
				}
				return 11, nil
			}
			return 4, nil
		case 6:
			c.setReg(m, y, c.fetch(m), idx, addr)
			if y == 6 {
				if idx != 0 {
					return 15, nil
				}
				return 10, nil
			}
			return 7, nil
		case 7:
			if y < 4 {
				f := c.F
				c.A = uint8(c.rotate(y, int(c.A)))
				c.F = uint8((int(f) & (FlagS | FlagZ | FlagP)) | (int(c.F) & FlagC) | (int(c.A) & 0x28))
			} else if y == 4 { // DAA
				a := int(c.A)
				f := int(c.F)
				correction := 0
				carry := (f&FlagC) != 0 || ((f&FlagN) == 0 && a > 0x99)
				if (f&FlagH) != 0 || ((f&FlagN) == 0 && (a&15) > 9) {
					correction |= 6
				}
				if carry {
					correction |= 0x60
				}
				na := a + correction
				if f&FlagN != 0 {
					na = a - correction
				}
				c.A = uint8(na & 255)
				nf := szp(int(c.A)) | (f & FlagN) | ((a ^ int(c.A)) & 16)
				if carry {
					nf |= FlagC
				}
				c.F = uint8(nf)
			} else if y == 5 { // CPL
				c.A ^= 255
				c.F = uint8((int(c.F) & (FlagS | FlagZ | FlagP | FlagC)) | FlagH | FlagN | (int(c.A) & 0x28))
			} else if y == 6 { // SCF
				c.F = uint8((int(c.F) & (FlagS | FlagZ | FlagP)) | FlagC | (int(c.A) & 0x28))
			} else { // CCF
				hc := FlagC
				if c.F&FlagC != 0 {
					hc = FlagH
				}
				c.F = uint8((int(c.F) & (FlagS | FlagZ | FlagP)) | hc | (int(c.A) & 0x28))
			}
			return 4, nil
		}
	}
	if x == 1 {
		if op == 0x76 {
			c.Halted = true
			return 4, nil
		}
		mem := y == 6 || z == 6
		var v int
		if mem {
			v = c.reg(m, z, 0, addr)
		} else {
			v = c.reg(m, z, idx, addr)
		}
		if mem {
			c.setReg(m, y, v, 0, addr)
		} else {
			c.setReg(m, y, v, idx, addr)
		}
		if mem {
			if idx != 0 {
				return 15, nil
			}
			return 7, nil
		}
		return 4, nil
	}
	if x == 2 {
		c.alu(y, c.reg(m, z, idx, addr))
		if z == 6 {
			if idx != 0 {
				return 15, nil
			}
			return 7, nil
		}
		return 4, nil
	}
	switch z {
	case 0:
		if c.cond(y) {
			c.PC = uint16(c.pop(m))
			return 11, nil
		}
		return 5, nil
	case 1:
		if q == 0 {
			v := c.pop(m)
			if p == 3 {
				c.A = uint8(v >> 8)
				c.F = uint8(v & 255)
			} else {
				c.setPair(p, v, idx)
			}
			return 10, nil
		}
		switch p {
		case 0:
			c.PC = uint16(c.pop(m))
			return 10, nil
		case 1: // EXX
			c.B, c.B2 = c.B2, c.B
			c.C, c.C2 = c.C2, c.C
			c.D, c.D2 = c.D2, c.D
			c.E, c.E2 = c.E2, c.E
			c.H, c.H2 = c.H2, c.H
			c.L, c.L2 = c.L2, c.L
			return 4, nil
		case 2:
			c.PC = uint16(c.pair(2, idx))
			return 4, nil
		default:
			c.SP = uint16(c.pair(2, idx))
			return 6, nil
		}
	case 2:
		a := c.nextWord(m)
		if c.cond(y) {
			c.PC = uint16(a)
		}
		return 10, nil
	case 3:
		switch y {
		case 0:
			c.PC = uint16(c.nextWord(m))
			return 10, nil
		case 1:
			c.bumpR()
			return c.cb(m, c.fetch(m), 0, -1), nil
		case 2:
			n := c.fetch(m)
			m.Output((int(c.A)<<8)|n, int(c.A))
			return 11, nil
		case 3:
			n := c.fetch(m)
			c.A = uint8(m.Input((int(c.A) << 8) | n))
			return 11, nil
		case 4: // EX (SP),HL/idx
			v := c.word(m, int(c.SP))
			c.putWord(m, int(c.SP), c.pair(2, idx))
			c.setPair(2, v, idx)
			return 19, nil
		case 5: // EX DE,HL
			c.D, c.H = c.H, c.D
			c.E, c.L = c.L, c.E
			return 4, nil
		case 6:
			c.IFF1, c.IFF2 = false, false
			return 4, nil
		case 7:
			c.IFF1, c.IFF2 = true, true
			c.EIDelay = 2
			return 4, nil
		}
	case 4:
		a := c.nextWord(m)
		if c.cond(y) {
			c.push(m, int(c.PC))
			c.PC = uint16(a)
			return 17, nil
		}
		return 10, nil
	case 5:
		if q == 0 {
			if p == 3 {
				c.push(m, int(c.A)<<8|int(c.F))
			} else {
				c.push(m, c.pair(p, idx))
			}
			return 11, nil
		}
		if p == 0 {
			a := c.nextWord(m)
			c.push(m, int(c.PC))
			c.PC = uint16(a)
			return 17, nil
		}
	case 6:
		c.alu(y, c.fetch(m))
		return 7, nil
	case 7:
		c.push(m, int(c.PC))
		c.PC = uint16(y * 8)
		return 11, nil
	}
	return 0, errPlain("Unsupported opcode " + strconv.FormatInt(int64(op), 16))
}
