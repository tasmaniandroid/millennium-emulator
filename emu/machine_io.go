package emu

import "math"

// Input performs an IN instruction with full device side
// effects. Decode ORDER IS LOAD-BEARING — it is a first-match chain exactly
// chain, and boards deliberately alias ports
// (on MTR2, 0x40 is both control-out and status1-in, 0x41 both PIO A out and
// status2 in), so the chain order defines what firmware observes. Reads of
// 8/9 CONSUME an ASCI receive byte and re-arm its timer; a read of 0x10
// latches the timers for the subsequent 0x0c/0x0d reads.
//
// Status bit layouts (computed fresh, NOT OR-ed over the port latch):
//
//	status1: eeOut bit in bit 3 (or 8 when idle) | 16 | MTR1: 64 + cashbox
//	         presence in bit 7 | cover-closed in bit 5 | CP/CFS sensor bits
//	status2: 64 when the telephony RX path is idle | 128 when ringing |
//	         modem read-back bit shifted into bit 4 | active DTMF in bits
//	         0-3 with 32 as its strobe
func (m *Machine) Input(port int) int {
	if m.InputOverride != nil {
		return m.InputOverride(port)
	}
	p := port & 255
	d := m.Devices
	b := m.Board
	v := int(m.Ports[p])
	cycles := float64(m.CPU.Cycles)
	switch {
	case b.Family == "mtr2" && p >= 0xe0 && p <= 0xe7:
		v = m.CoinUARTInput(p)
	case b.Family == "mtr2" && p == 0xc0:
		v = (cardInput(m) << 1) | boolInt(!d.CashboxRemoved, 12)
	case b.Family == "mtr2" && p == 0xc1:
		v = 0xff
	case p == b.Status1:
		ee := 8
		if len(d.EeOut) > 0 {
			ee = d.EeOut[0] << 3
		}
		v = ee | 16
		if b.Family == "mtr1" {
			v |= 64 | boolInt(!d.CashboxRemoved, 128)
		}
		v |= boolInt(!d.CoverOpen, 32) | cardSensors(d.Reader)
	case p == b.Status2:
		// JS assigns a fully computed value here; the stale port byte must
		// not leak in (0x41 doubles as PIO A on MTR2).
		v = 0
		if len(d.IpQueue) == 0 && len(d.IpRxActive) == 0 {
			v |= 64
		}
		if d.Ring {
			v |= 128
		}
		v |= modemStatusBit(m) << 4
		if d.Modem.DTMF != nil && cycles < d.Modem.DTMFUntil {
			v |= 32 | *d.Modem.DTMF
		}
	case b.Family == "mtr1" && p == 0x42:
		v = (v & ^4) | (cardInput(m) << 2)
	case p == b.Display:
		v = 0 // VFD ready, not busy
	case p == 4 || p == 5:
		ch := p - 4
		v &= 9
		if cycles >= d.AsciNextTx[ch] {
			v |= 2
		}
		if len(d.AsciRx[ch]) > 0 && cycles >= d.AsciNextRx[ch] {
			v |= 128
		}
	case p == 8 || p == 9:
		ch := p - 8
		if len(d.AsciRx[ch]) > 0 {
			v = int(d.AsciRx[ch][0])
			d.AsciRx[ch] = d.AsciRx[ch][1:]
		} else {
			v = 0
		}
		d.AsciNextRx[ch] = cycles + float64(m.SerialCycles(ch))
	case p == 0x10:
		d.TimerRead = true
	case (p == 0x0c || p == 0x0d) && d.TimerRead:
		m.Ports[0x10] &= ^byte(64)
		d.TimerRead = false
	case p == 0x0b:
		d.CsioInterrupt = false
	}
	if len(m.Watchpoints) > 0 {
		m.watch("in", p, p, v)
	}
	return v
}

// SerialCycles returns one ASCI character time in T-states: fixed rates on
// MTR1, divisor/framing-derived on MTR2.
func (m *Machine) SerialCycles(ch int) int {
	if m.Board.Family == "mtr1" {
		if ch == 0 {
			return 102400
		}
		return 61440
	}
	n := int(m.Ports[2+ch])
	a := int(m.Ports[ch])
	base := 10
	if n&32 != 0 {
		base = 30
	}
	mult := 16
	if n&8 != 0 {
		mult = 64
	}
	divisor := base * (1 << (n & 7)) * mult
	frame := 1
	if a&4 != 0 {
		frame += 8
	} else {
		frame += 7
	}
	if a&2 != 0 {
		frame++
	}
	if a&1 != 0 {
		frame += 2
	} else {
		frame++
	}
	return int(math.Max(1, float64(divisor*frame)))
}

// Output performs an OUT instruction with device side effects. Writes to
// MMU registers 0x38–0x3a refresh the cached address translation.
// Notable edges: the control port handles EEPROM chip-select gating,
// telephony power-restore scheduling, the CSI/O clock-edge counter and relay
// changes; PIO B bit 4 falling edge resets the VFD and its eeClock bit
// (rising, with CS high) clocks one EEPROM bit; 0x06/0x07 are the ASCI0/1
// transmit registers (ASCI0 doubles as the coin-validator link on MTR1).
func (m *Machine) Output(port, value int) error {
	if m.OutputOverride != nil {
		m.OutputOverride(port, value)
		return nil
	}
	p := port & 255
	v := value & 255
	old := int(m.Ports[p])
	d := m.Devices
	b := m.Board
	m.Ports[p] = byte(v)
	if p == 0x38 || p == 0x39 || p == 0x3a {
		m.refreshMMUCache()
	}
	if len(m.Watchpoints) > 0 {
		m.watch("out", p, p, v)
	}
	if b.Family == "mtr2" && p >= 0xe0 && p <= 0xe7 {
		m.CoinUARTOutput(p, v)
	}
	if p == b.PioA {
		modemOutput(m, v, old)
	}
	smartOutput(m, p, v, old)
	if p == b.Display && (int(m.Ports[b.PioB])&64) == 0 {
		m.DisplayWrite(v, (int(m.Ports[b.PioB])&32) != 0)
	}
	if p == b.Control {
		if v&64 == 0 { // Microwire chip deselect
			d.EeBits = []int{}
			d.EeOut = []int{}
			d.EeRead = false
			d.EeData = []int{}
		}
		if (v&32) != 0 && (old&32) == 0 { // power restored
			due := float64(m.CPU.Cycles) + m.MsCycles(50)
			d.TelephonyResetDue = &due
		}
		if (v&128) != 0 && (old&128) == 0 && d.SerialPending != 0 {
			d.SerialEdges++
			if d.SerialEdges >= 8 {
				if err := m.CompleteSerial(); err != nil {
					return err
				}
			}
		}
		if (old^v)&7 != 0 {
			m.Event("relays", Ordered("value", v&7))
			modemRelays(m, v, old)
		}
	}
	if p == b.PioB {
		if (v&16) == 0 && (old&16) != 0 {
			resetVFD(&d.VFDState)
		}
		if (v&b.EeClock) != 0 && (old&b.EeClock) == 0 && (int(m.Ports[b.Control])&64) != 0 {
			m.EepromClock()
		}
	}
	if p == 0x0a {
		if v&48 != 0 {
			d.SerialPending = v & 48
			d.SerialEdges = 0
		} else {
			d.SerialPending = 0
			d.CsioInterrupt = false
		}
	}
	if p == 0x06 || p == 0x07 {
		ch := p - 6
		d.AsciNextTx[ch] = float64(m.CPU.Cycles) + float64(m.SerialCycles(ch))
		d.AsciTx[ch] = append(d.AsciTx[ch], byte(v))
		if len(d.AsciTx[ch]) > 4096 {
			d.AsciTx[ch] = d.AsciTx[ch][1:]
		}
		m.Event("serial.tx", Ordered("channel", ch, "value", v))
		if ch == 0 && b.Family == "mtr1" {
			m.CoinByte(byte(v))
		}
		if m.OnSerialTx != nil {
			m.OnSerialTx(ch, byte(v))
		}
	}
	if p == b.Voice {
		d.Voice = append(d.Voice, byte(v))
		if len(d.Voice) > 128 {
			d.Voice = d.Voice[1:]
		}
		bank := 0
		if b.Family == "mtr1" {
			bank = (int(m.Ports[b.PioB]) >> 1) & 7
		} else {
			bank = int(m.Ports[b.PioB]) & 15
		}
		m.Event("voice", Ordered("bank", bank, "code", v))
	}
	if p == 0x30 {
		if v&64 == 0 {
			d.Dma0Interrupt = false
		} else if int(m.Ports[0x31])&12 != 12 {
			m.DMA()
		}
	}
	return nil
}

func (m *Machine) DisplayWrite(v int, command bool) {
	WriteVFD(m, &m.Devices.VFDState, v, command)
}

var glyphTable = map[byte]rune{
	0xa1: 'à', 0xa2: 'á', 0xa6: 'é', 0xa9: 'í', 0xac: 'ç',
	0xae: 'ú', 0xb1: 'ó', 0xb4: 'ñ', 0xb6: '¢', 0xb7: '◆',
	0xb8: '█', 0xb9: 'S', 0xff: '°',
}

func glyph(b byte) rune {
	if r, ok := glyphTable[b]; ok {
		return r
	}
	if b >= 32 {
		return rune(b)
	}
	return ' '
}

func glyphLine(data ByteList) string {
	out := make([]rune, len(data))
	for i, b := range data {
		out[i] = glyph(b)
	}
	return string(out)
}

// DisplayStateData holds the two glyph-mapped text lines and VFD parameters.
type DisplayStateData struct {
	Display         [2]string       `json:"display"`
	DisplayHardware DisplayHardware `json:"displayHardware"`
}

type DisplayHardware struct {
	Mode           int   `json:"mode"`
	Cursor         int   `json:"cursor"`
	CursorVisible  bool  `json:"cursorVisible"`
	ScrollPending  bool  `json:"scrollPending"`
	BlinkPositions []int `json:"blinkPositions"`
	Intensity      int   `json:"intensity"`
	Revision       int64 `json:"revision"`
}

func (m *Machine) DisplayState() DisplayStateData {
	d := m.Devices
	return DisplayStateData{
		Display: [2]string{glyphLine(d.Display[:20]), glyphLine(d.Display[20:])},
		DisplayHardware: DisplayHardware{
			Mode: d.DisplayMode, Cursor: d.Cursor, CursorVisible: d.DisplayCursorVisible,
			ScrollPending: d.DisplayScrollPending, BlinkPositions: d.DisplayBlinkPositions,
			Intensity: d.DisplayIntensity, Revision: d.DisplayRevision,
		},
	}
}

// EepromClock advances the bit-clocked Microwire protocol (93C46 on MTR1,
// 93C66 on MTR2) by one clock. Framing: a start bit 1, a 2-bit opcode and
// an address, MSB first — 9 header bits for 64-word parts, 11 for 256-word.
// Opcodes: 00 = write-enable subcommands (address high bits select EWEN/
// EWDS/ERAL/WRAL), 01 = write (16 data bits follow, committed only when
// write-enabled and the data frame's two leading bits are 01), 10 = read
// (shifts out a leading zero then the 16 word bits on subsequent clocks),
// 11 = erase word. Chip-select falling edge (handled in Output) aborts any
// partial frame. Writes complete immediately in simulated time.
func (m *Machine) EepromClock() {
	d := m.Devices
	if d.EeRead {
		if len(d.EeOut) > 0 {
			d.EeOut = d.EeOut[1:]
		}
		return
	}
	bits := 6
	if m.Board.EeWords == 256 {
		bits = 8
	}
	header := bits + 3
	bit := int(m.Ports[m.Board.PioA]) & 1
	if len(d.EeBits) == 0 && bit == 0 {
		return
	}
	d.EeBits = append(d.EeBits, bit)
	if len(d.EeBits) == header {
		cmd := 0
		for _, b := range d.EeBits {
			cmd = (cmd << 1) | b
		}
		op := (cmd >> bits) & 3
		addr := cmd & (m.Board.EeWords - 1)
		d.EeAddress = addr
		if op == 2 {
			word := int(d.Eeprom[addr*2])<<8 | int(d.Eeprom[addr*2+1])
			out := make([]int, 0, 17)
			out = append(out, 0)
			for i := 15; i >= 0; i-- {
				out = append(out, (word>>i)&1)
			}
			d.EeOut = out
			d.EeRead = true
		} else if op == 0 {
			sub := addr >> (bits - 2)
			if sub == 3 {
				d.EeWriteEnabled = true
			} else if sub == 0 {
				d.EeWriteEnabled = false
			} else if sub == 2 && d.EeWriteEnabled {
				for i := range d.Eeprom {
					d.Eeprom[i] = 255
				}
			}
		} else if op == 3 && d.EeWriteEnabled {
			d.Eeprom[addr*2] = 255
			d.Eeprom[addr*2+1] = 255
		}
	} else if len(d.EeBits) == header+16 && ((d.EeBits[1]<<1)|d.EeBits[2]) == 1 && d.EeWriteEnabled {
		word := 0
		for _, b := range d.EeBits[header:] {
			word = (word << 1) | b
		}
		d.Eeprom[d.EeAddress*2] = byte(word >> 8)
		d.Eeprom[d.EeAddress*2+1] = byte(word & 255)
	}
}

// CompleteSerial transfers one byte across the clocked telephony interface
// (CSI/O). Port 0x0a arms a transfer mode; eight clock edges on control
// bit 7 complete it. In receive mode (0x20 armed) the next queued message is
// shifted out one byte per completed transfer through port 0x0b; in transmit
// mode the byte in 0x0b is fed to the telephony accumulator. The CSIO
// interrupt flag re-arms when the control register still has bit 6 set.
func (m *Machine) CompleteSerial() error {
	d := m.Devices
	mode := d.SerialPending
	d.SerialPending = 0
	d.SerialEdges = 0
	m.Ports[0x0a] &= ^byte(48)
	if mode&32 != 0 {
		if len(d.IpRxActive) == 0 {
			if len(d.IpQueue) > 0 {
				d.IpRxActive = d.IpQueue[0]
				d.IpQueue = d.IpQueue[1:]
			} else {
				d.IpRxActive = ByteList{0}
			}
		}
		if len(d.IpRxActive) > 0 {
			m.Ports[0x0b] = d.IpRxActive[0]
			d.IpRxActive = d.IpRxActive[1:]
		} else {
			m.Ports[0x0b] = 0
		}
	} else {
		if err := m.TelephonyByte(int(m.Ports[0x0b])); err != nil {
			return err
		}
	}
	if m.Ports[0x0a]&64 != 0 {
		d.CsioInterrupt = true
	}
	return nil
}

// TelephonyByte accumulates transmit bytes from the firmware into complete
// telephony messages and answers them like the simulated line-card processor.
// Framing: commands >= 0xc0 are length-prefixed (byte 2 = total length);
// shorter commands are single-byte. Responses longer than one byte carry a
// trailing additive checksum. Keypad commands 0x10..0x1f map through
// "A1234567890*#BCD"; 0x40..0x46 set the call state; 0x32 clears dial state.
// Unknown commands are recorded (bounded list) and surfaced as events rather
// than silently dropped.
func (m *Machine) TelephonyByte(v int) error {
	d := m.Devices
	d.IpTx = append(d.IpTx, byte(v))
	if d.IpTx[0] >= 0xc0 {
		if len(d.IpTx) < 2 || len(d.IpTx) < int(d.IpTx[1]) {
			return nil
		}
	}
	msg := d.IpTx
	d.IpTx = ByteList{}
	cmd := int(msg[0])
	m.Event("telephony.tx", Ordered("bytes", msg))
	if cmd == 0xc0 {
		return m.QueueIP([]byte{0x74})
	}
	var reply []byte
	switch cmd {
	case 0x30:
		if d.OffHook {
			reply = []byte{0x6e}
		} else {
			reply = []byte{0x6c}
		}
	case 0x31:
		reply = []byte{0xc0, 8, byte(d.CallState), 0, 0, 0, 0, 0}
	case 0x33:
		reply = []byte{0x7a}
	case 0x34:
		reply = append([]byte{0xc2, 14}, []byte("EMU 01.00   ")...)
	case 0x37:
		switch {
		case d.LineConnected && d.Reversed:
			reply = []byte{0x86}
		case d.LineConnected:
			reply = []byte{0x84}
		case d.Reversed:
			reply = []byte{0x82}
		default:
			reply = []byte{0x80}
		}
	case 0x38:
		reply = []byte{0xc4, 4, 0, 0}
	case 0x3a:
		reply = []byte{0x88}
	case 0x3b:
		reply = []byte{0x8c}
	}
	if reply != nil {
		if reply[0] >= 0xc0 {
			sum := 0
			for _, b := range reply[:len(reply)-1] {
				sum = (sum + int(b)) & 255
			}
			reply[len(reply)-1] = byte(sum)
		}
		return m.QueueIP(reply)
	}
	if cmd >= 0x10 && cmd <= 0x1f {
		digit := string("A1234567890*#BCD"[cmd-0x10])
		feedback := (m.RelayState() & 1) != 0
		if !feedback {
			d.Dialed += digit
		}
		m.Event("dial", Ordered("number", d.Dialed, "digit", digit, "feedback", feedback))
	} else if cmd >= 0x40 && cmd <= 0x46 {
		d.CallState = cmd - 0x40
	} else if cmd == 0x32 {
		d.Dialed = ""
		d.Modem.Digits = ""
		d.CallState = 0
	} else if !(cmd == 0 || cmd == 0x72 || cmd == 0xc1 || (cmd >= 0x20 && cmd <= 0x3e)) {
		d.UnknownCommands = append(d.UnknownCommands, byte(cmd))
		if len(d.UnknownCommands) > 100 {
			d.UnknownCommands = d.UnknownCommands[1:]
		}
		m.Event("telephony.unsupported", Ordered("command", cmd))
	}
	return nil
}
