// Package disasm renders Z80/Z180 instruction mnemonics for the debugger
// views, including the Z180 extensions and DD/FD indexed forms. The exact
// text formatting is part of the API: the browser and scripts pattern-match
// these strings.
package disasm

import (
	"encoding/hex"
	"strconv"
)

type Row struct {
	Address int    `json:"address"`
	Bytes   string `json:"bytes"`
	Text    string `json:"text"`
}

func hx(n, length int) string {
	s := strconv.FormatInt(int64(n), 16)
	for len(s) < length {
		s = "0" + s
	}
	return "0x" + s
}

var conditions = []string{"NZ", "Z", "NC", "C", "PO", "PE", "P", "M"}
var aluNames = []string{"ADD A,", "ADC A,", "SUB ", "SBC A,", "AND ", "XOR ", "OR ", "CP "}

// Disassemble decodes count instructions starting at logical address start.
func Disassemble(read func(int) int, start, count int) []Row {
	rows := []Row{}
	pc := start
	for n := 0; n < count; n++ {
		address := pc
		raw := []byte{}
		one := func() int {
			v := read(pc & 65535)
			pc = (pc + 1) & 65535
			raw = append(raw, byte(v))
			return v
		}
		word := func() string {
			l := one()
			return hx(l|one()<<8, 4)
		}
		relative := func() string {
			d := one()
			if d&128 != 0 {
				d -= 256
			}
			return hx((pc+d)&65535, 4)
		}
		op := one()
		idx := ""
		prefixes := 0
		for (op == 0xdd || op == 0xfd) && prefixes < 16 {
			prefixes++
			if op == 0xdd {
				idx = "IX"
			} else {
				idx = "IY"
			}
			op = one()
		}
		x := op >> 6
		y := (op >> 3) & 7
		z := op & 7
		p := y >> 1
		q := y & 1
		pair2 := "HL"
		if idx != "" {
			pair2 = idx
		}
		pairs := []string{"BC", "DE", pair2, "SP"}
		mem := "(HL)"
		if idx != "" {
			mem = "(" + idx + ")"
		}
		if idx != "" && ((x == 0 && (z == 4 || z == 5 || z == 6) && y == 6) ||
			(x == 1 && op != 0x76 && (y == 6 || z == 6)) || (x == 2 && z == 6)) {
			d := one()
			if d&128 != 0 {
				d -= 256
			}
			sign := "+"
			abs := d
			if d < 0 {
				sign = "-"
				abs = -d
			}
			mem = "(" + idx + sign + hx(abs, 2) + ")"
		}
		regH, regL := "H", "L"
		if idx != "" {
			regH, regL = idx+"H", idx+"L"
		}
		regs := []string{"B", "C", "D", "E", regH, regL, mem, "A"}
		text := ""
		cb := func(c int, m string, indexed bool) string {
			a := c >> 6
			b := (c >> 3) & 7
			r := c & 7
			target := regs[r]
			if indexed {
				target = m
			}
			plain := []string{"B", "C", "D", "E", "H", "L", "", "A"}
			if a == 0 {
				suffix := ""
				if indexed && r != 6 {
					suffix = "," + plain[r]
				}
				return []string{"RLC", "RRC", "RL", "RR", "SLA", "SRA", "SLL", "SRL"}[b] + " " + target + suffix
			}
			suffix := ""
			if indexed && a != 1 && r != 6 {
				suffix = "," + plain[r]
			}
			return []string{"", "BIT", "RES", "SET"}[a] + " " + strconv.Itoa(b) + "," + target + suffix
		}
		if op == 0xcb {
			if idx != "" {
				d := one()
				if d&128 != 0 {
					d -= 256
				}
				sign := "+"
				abs := d
				if d < 0 {
					sign = "-"
					abs = -d
				}
				text = cb(one(), "("+idx+sign+hx(abs, 2)+")", true)
			} else {
				text = cb(one(), "", false)
			}
		} else if op == 0xed {
			e := one()
			ey := (e >> 3) & 7
			ep := ey >> 1
			eq := ey & 1
			rr := []string{"B", "C", "D", "E", "H", "L", "(HL)", "A"}
			pp := []string{"BC", "DE", "HL", "SP"}
			switch {
			case e < 64 && (e&7) == 0:
				text = "IN0 " + rr[ey] + ",(" + hx(one(), 2) + ")"
			case e < 64 && (e&7) == 1:
				r := rr[ey]
				if ey == 6 {
					r = "0"
				}
				text = "OUT0 (" + hx(one(), 2) + ")," + r
			case e < 64 && (e&7) == 4:
				text = "TST " + rr[ey]
			case e == 0x64 || e == 0x74:
				name := "TSTIO"
				if e == 0x64 {
					name = "TST"
				}
				text = name + " " + hx(one(), 2)
			case e == 0x4c || e == 0x5c || e == 0x6c || e == 0x7c:
				text = "MLT " + pp[ep]
			case e == 0x76:
				text = "SLP"
			case e == 0x83 || e == 0x8b || e == 0x93 || e == 0x9b:
				text = map[int]string{0x83: "OTIM", 0x8b: "OTDM", 0x93: "OTIMR", 0x9b: "OTDMR"}[e]
			case (e & 0xc0) == 0x40:
				switch e & 7 {
				case 0:
					text = "IN " + rr[ey] + ",(C)"
				case 1:
					r := rr[ey]
					if ey == 6 {
						r = "0"
					}
					text = "OUT (C)," + r
				case 2:
					n := "ADC"
					if eq == 0 {
						n = "SBC"
					}
					text = n + " HL," + pp[ep]
				case 3:
					a := word()
					if eq != 0 {
						text = "LD " + pp[ep] + ",(" + a + ")"
					} else {
						text = "LD (" + a + ")," + pp[ep]
					}
				case 4:
					text = "NEG"
				case 5:
					if e == 0x4d {
						text = "RETI"
					} else {
						text = "RETN"
					}
				case 6:
					text = "IM " + strconv.Itoa([]int{0, 0, 1, 2, 0, 0, 1, 2}[ey])
				case 7:
					text = []string{"LD I,A", "LD R,A", "LD A,I", "LD A,R", "RRD", "RLD", "?", "?"}[ey]
				}
			default:
				if t, ok := map[int]string{0xa0: "LDI", 0xa1: "CPI", 0xa2: "INI", 0xa3: "OUTI",
					0xa8: "LDD", 0xa9: "CPD", 0xaa: "IND", 0xab: "OUTD",
					0xb0: "LDIR", 0xb1: "CPIR", 0xb2: "INIR", 0xb3: "OTIR",
					0xb8: "LDDR", 0xb9: "CPDR", 0xba: "INDR", 0xbb: "OTDR"}[e]; ok {
					text = t
				} else {
					text = "ILLEGAL ED " + hx(e, 2)
				}
			}
		} else if x == 0 {
			switch z {
			case 0:
				switch {
				case y == 0:
					text = "NOP"
				case y == 1:
					text = "EX AF,AF'"
				case y == 2:
					text = "DJNZ " + relative()
				case y == 3:
					text = "JR " + relative()
				default:
					text = "JR " + conditions[y-4] + "," + relative()
				}
			case 1:
				if q != 0 {
					text = "ADD " + pairs[2] + "," + pairs[p]
				} else {
					text = "LD " + pairs[p] + "," + word()
				}
			case 2:
				if p < 2 {
					if q != 0 {
						text = "LD A,(" + pairs[p] + ")"
					} else {
						text = "LD (" + pairs[p] + "),A"
					}
				} else {
					a := word()
					r := "A"
					if p == 2 {
						r = pairs[2]
					}
					if q != 0 {
						text = "LD " + r + ",(" + a + ")"
					} else {
						text = "LD (" + a + ")," + r
					}
				}
			case 3:
				n := "INC"
				if q != 0 {
					n = "DEC"
				}
				text = n + " " + pairs[p]
			case 4:
				text = "INC " + regs[y]
			case 5:
				text = "DEC " + regs[y]
			case 6:
				text = "LD " + regs[y] + "," + hx(one(), 2)
			case 7:
				text = []string{"RLCA", "RRCA", "RLA", "RRA", "DAA", "CPL", "SCF", "CCF"}[y]
			}
		} else if x == 1 {
			r := []string{"B", "C", "D", "E", "H", "L", mem, "A"}
			if op == 0x76 {
				text = "HALT"
			} else {
				dst := regs[y]
				src := regs[z]
				if y == 6 || z == 6 {
					dst = r[y]
					src = r[z]
				}
				text = "LD " + dst + "," + src
			}
		} else if x == 2 {
			text = aluNames[y] + regs[z]
		} else {
			switch z {
			case 0:
				text = "RET " + conditions[y]
			case 1:
				if q == 0 {
					n := pairs[p]
					if p == 3 {
						n = "AF"
					}
					text = "POP " + n
				} else {
					text = []string{"RET", "EXX", "JP (" + pairs[2] + ")", "LD SP," + pairs[2]}[p]
				}
			case 2:
				text = "JP " + conditions[y] + "," + word()
			case 3:
				switch y {
				case 0:
					text = "JP " + word()
				case 2:
					text = "OUT (" + hx(one(), 2) + "),A"
				case 3:
					text = "IN A,(" + hx(one(), 2) + ")"
				default:
					text = []string{"", "", "", "", "EX (SP)," + pairs[2], "EX DE,HL", "DI", "EI"}[y]
				}
			case 4:
				text = "CALL " + conditions[y] + "," + word()
			case 5:
				if q == 0 {
					n := pairs[p]
					if p == 3 {
						n = "AF"
					}
					text = "PUSH " + n
				} else if p == 0 {
					text = "CALL " + word()
				} else {
					text = "PREFIX"
				}
			case 6:
				text = aluNames[y] + hx(one(), 2)
			case 7:
				text = "RST " + hx(y*8, 2)
			}
		}
		rows = append(rows, Row{Address: address, Bytes: hex.EncodeToString(raw), Text: text})
	}
	return rows
}
