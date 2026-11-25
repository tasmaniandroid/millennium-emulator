package emu

import "strings"

// Board wiring is independent of the firmware edition and its symbol map.
type Board struct {
	ID            string `json:"id"`
	Family        string `json:"family"`
	ClockHz       int    `json:"clockHz"`
	MaxROM        int    `json:"maxROM"`
	RamStart      int    `json:"ramStart"`
	Control       int    `json:"control"`
	PioA          int    `json:"pioA"`
	PioB          int    `json:"pioB"`
	PioC          int    `json:"pioC"`
	Status1       int    `json:"status1"`
	Status2       int    `json:"status2"`
	Display       int    `json:"display"`
	Voice         int    `json:"voice"`
	EeClock       int    `json:"eeClock"`
	EeWords       int    `json:"eeWords"`
	CardDMA       int    `json:"cardDMA"`
	CardInputPort int    `json:"cardInputPort"`
	CardInputBit  int    `json:"cardInputBit"`
}

var boardOrder = []string{"mtr1", "mtr2", "mtr1-coin-basic"}

var Boards = map[string]*Board{
	"mtr1": {ID: "mtr1", Family: "mtr1", ClockHz: 6144000, MaxROM: 0x40000, RamStart: 0x40000,
		Control: 0x60, PioA: 0x40, PioB: 0x41, PioC: 0x42, Status1: 0x80, Status2: 0xe0, Display: 0xa0, Voice: 0xc0,
		EeClock: 1, EeWords: 64, CardDMA: 0, CardInputPort: 0x42, CardInputBit: 2},
	"mtr2": {ID: "mtr2", Family: "mtr2", ClockHz: 12288000, MaxROM: 0x100000, RamStart: 0xc0000,
		Control: 0x40, PioA: 0x41, PioB: 0x42, PioC: 0x43, Status1: 0x40, Status2: 0x41, Display: 0x60, Voice: 0x61,
		EeClock: 128, EeWords: 256, CardDMA: 1, CardInputPort: 0xc0, CardInputBit: 1},
}

func init() {
	// The Coin Basic ROM sends its VFD data and commands to 0xc0, not 0xa0.
	cb := *Boards["mtr1"]
	cb.ID = "mtr1-coin-basic"
	cb.Display = 0xc0
	cb.Voice = 0xa0
	Boards["mtr1-coin-basic"] = &cb
}

// BoardValues returns boards in JS Object.values insertion order.
func BoardValues() []*Board {
	out := make([]*Board, 0, len(boardOrder))
	for _, id := range boardOrder {
		out = append(out, Boards[id])
	}
	return out
}

func SelectBoard(id string, size int) (*Board, error) {
	if id == "" || id == "auto" {
		if size > 0x40000 {
			id = "mtr2"
		} else {
			id = "mtr1"
		}
	}
	b, ok := Boards[id]
	if !ok {
		return nil, errf("board must be auto or one of: %s", strings.Join(boardOrder, ", "))
	}
	return b, nil
}

// CoinUART models the MTR2 external coin-validator UART (ports 0xe0..0xe7),
// a 16550-style register file: 0xe0 is RX/TX data (reads consume from the
// shared ASCI0 receive queue and re-arm a 10/600-second baud pacing timer;
// writes forward to the validator model), 0xe2 is the line status register
// (bit 2 = RX ready gated by the enable, bit 1 = TX ready, bit 0 = busy),
// 0xe5 exposes the status high bits, and 0xe3 bit 7 (DLAB) swaps the 16-bit
// baud divisor through 0xe0/0xe1 instead of data.
type CoinUART struct {
	Registers ByteList `json:"registers"`
	Divisor   int      `json:"divisor"`
	NextRX    float64  `json:"nextRX"`
	NextTX    float64  `json:"nextTX"`
}

func CoinUARTState() *CoinUART {
	return &CoinUART{Registers: make(ByteList, 8), Divisor: 0x280}
}

func (m *Machine) CoinUARTInput(p int) int {
	u := m.Devices.CoinUART
	r := u.Registers
	offset := p - 0xe0
	rx := &m.Devices.AsciRx[0]
	if (r[3]&128) != 0 && offset < 2 {
		if offset != 0 {
			return int(u.Divisor >> 8)
		}
		return int(u.Divisor & 255)
	}
	if offset == 0 {
		u.NextRX = float64(m.CPU.Cycles) + m.MsCycles(1000*10.0/600)
		if len(*rx) > 0 {
			v := (*rx)[0]
			*rx = (*rx)[1:]
			return int(v)
		}
		return 0
	}
	if offset == 2 {
		if len(*rx) > 0 && float64(m.CPU.Cycles) >= u.NextRX && (r[1]&1) != 0 {
			return 4
		}
		if (r[1]&2) != 0 && float64(m.CPU.Cycles) >= u.NextTX {
			return 2
		}
		return 1
	}
	if offset == 5 {
		bit := 0
		if len(*rx) > 0 && float64(m.CPU.Cycles) >= u.NextRX {
			bit = 1
		}
		return 0x60 | bit
	}
	return int(r[offset])
}

func (m *Machine) CoinUARTOutput(p int, v int) {
	u := m.Devices.CoinUART
	r := u.Registers
	offset := p - 0xe0
	if (r[3]&128) != 0 && offset < 2 {
		if offset != 0 {
			u.Divisor = (u.Divisor & 255) | (v << 8)
		} else {
			u.Divisor = (u.Divisor & 0xff00) | v
		}
		return
	}
	if offset == 0 {
		u.NextTX = float64(m.CPU.Cycles) + m.MsCycles(1000*10.0/600)
		m.CoinByte(byte(v))
		return
	}
	r[offset] = byte(v)
}

// FlashOffset maps a physical address to a flash image offset, or -1 when
// the address is RAM. The low 64 KiB is the CPU's reset window: port 0xc0
// bits 0-2 select which 64 KiB flash sector (C/D/E/F or sector 0) appears
// there, letting the bootstrap switch between itself, the DLA and main
// application without remapping RAM. The mid region maps 1:1. Addresses at
// or above Board.RamStart belong to the separate RAM device and must never
// alias into flash storage.
func (m *Machine) FlashOffset(p int) int {
	if p < 0x10000 {
		sel := int(m.Ports[0xc0] & 7)
		sector := []int{12, 13, 14, 15, 12, 13, 0, 0}[sel]
		return (sector << 16) | p
	}
	if p < 0xc0000 {
		return p
	}
	return -1
}

// FlashCommandState is one flash chip's command latch. Phase is an integer
// state 0..5, or the string "program" while a byte-program is armed;
// snapshots serialize both forms, so the field stays `any`.
type FlashCommandState struct {
	Phase any  `json:"phase"` // int 0..5 or the string "program"
	ID    bool `json:"id"`
}

func flashPhaseInt(s *FlashCommandState) int {
	if v, ok := s.Phase.(int); ok {
		return v
	}
	if v, ok := s.Phase.(float64); ok {
		return int(v)
	}
	return -1 // 'program'
}

func newFlashCommands() []*FlashCommandState {
	return []*FlashCommandState{{Phase: 0, ID: false}, {Phase: 0, ID: false}}
}

// FlashRead returns one flash byte, honoring ID mode: while a chip's ID
// latch is set, address 0 reads 0x01, address 1 reads 0xA4 (the AMD
// manufacturer/device signature this model reports) and everything else
// reads 0xFF.
func (m *Machine) FlashRead(offset int) int {
	state := m.Devices.FlashCommands[offset>>19]
	if state.ID {
		a := offset & 0x7ffff
		if a == 0 {
			return 0x01
		}
		if a == 1 {
			return 0xa4
		}
		return 0xff
	}
	return int(m.Flash[offset])
}

// FlashWrite advances the AMD-compatible command state machine for one chip
// (selected by offset>>19). Unlock sequence: 0xAA at x5555, 0x55 at x2AAA,
// then a command at x5555 — 0x90 enters ID mode, 0xA0 arms a single byte-
// program (AND semantics, immediate completion), 0x80 starts the erase
// sequence whose next unlock selects 0x30 (64 KiB sector) or 0x10 (whole
// 512 KiB chip). Writing 0xF0 anywhere returns to read mode; any write
// outside the expected sequence resets the latch. Busy/toggle timing is
// intentionally not modeled — completion is immediate in simulated time.
func (m *Machine) FlashWrite(offset int, value int) {
	chip := offset >> 19
	a := offset & 0x7ffff
	s := m.Devices.FlashCommands[chip]
	if flashPhaseInt(s) == -1 { // phase === 'program'
		m.Flash[offset] &= byte(value)
		s.Phase = 0
		m.Event("flash.program", Ordered("offset", offset, "value", int(m.Flash[offset])))
		return
	}
	ph := flashPhaseInt(s)
	if value == 0xf0 {
		s.Phase = 0
		s.ID = false
		return
	}
	if ph == 0 {
		if a == 0x5555 && value == 0xaa {
			s.Phase = 1
		}
		return
	}
	if ph == 1 {
		if a == 0x2aaa && value == 0x55 {
			s.Phase = 2
		} else {
			s.Phase = 0
		}
		return
	}
	if ph == 2 {
		s.Phase = 0
		if a != 0x5555 {
			return
		}
		if value == 0x90 {
			s.ID = true
		} else if value == 0xa0 {
			s.Phase = "program"
		} else if value == 0x80 {
			s.Phase = 3
		}
		return
	}
	if ph == 3 {
		if a == 0x5555 && value == 0xaa {
			s.Phase = 4
		} else {
			s.Phase = 0
		}
		return
	}
	if ph == 4 {
		if a == 0x2aaa && value == 0x55 {
			s.Phase = 5
		} else {
			s.Phase = 0
		}
		return
	}
	s.Phase = 0
	if value == 0x30 {
		start := offset &^ 0xffff
		for i := start; i < start+0x10000; i++ {
			m.Flash[i] = 255
		}
		m.Event("flash.erase", Ordered("offset", start, "length", 0x10000))
	} else if a == 0x5555 && value == 0x10 {
		start := chip << 19
		for i := start; i < start+0x80000; i++ {
			m.Flash[i] = 255
		}
		m.Event("flash.erase", Ordered("offset", start, "length", 0x80000))
	}
}
