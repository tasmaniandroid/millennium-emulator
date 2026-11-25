package emu

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var coinChars = "123456789abcdefg"

// CoinByte feeds one ASCI0 byte to the serial coin-validator model (MTR1;
// MTR2 uses the external UART which forwards here too). Protocol summary:
//
//	'g' (103)  wake-up probe            -> 'A' (all well)
//	'r' (114)  status request           -> 'B',0,0 (hardware/sensor faults)
//	'a'/'x'    accept enable/disable    (sets coinAccept)
//	'z'/'s'    reject (accept off)
//	'c' (99)   collect escrowed coins   -> 'A'
//	'f' (102)  refund escrowed coins    -> 'A'
//	'n' (110)  no-op acknowledge        -> 'A'
//	'l' (108)  learn/escrow: mechanical on the real validator; modeled as
//	           an event only, because the firmware waits on a timer
//	'd'/'e' (100/101) + index: enable/disable one of the 16 coin types
//	           (index < 16 numeric, or its character in coinChars)
//	'q' (113) + 2-byte address: read validator registers 0x1fc..0x1ff
//	           (responds 1,0,0,1) or 255 elsewhere
//
// A coin insertion via device event pushes its type character (accepted) or
// '0' (rejected) into the same ASCI0 receive queue.
func (m *Machine) CoinByte(v byte) {
	d := m.Devices
	d.CoinTx = append(d.CoinTx, v)
	if len(d.CoinTx) > 256 {
		d.CoinTx = d.CoinTx[1:]
	}
	if len(d.CoinCommand) > 0 {
		d.CoinCommand = append(d.CoinCommand, v)
		cmd := d.CoinCommand[0]
		if cmd == 113 && len(d.CoinCommand) == 3 {
			addr := int(d.CoinCommand[1])<<8 | int(v)
			if addr >= 0x1fc && addr <= 0x1ff {
				d.AsciRx[0] = append(d.AsciRx[0], []byte{1, 0, 0, 1}[addr-0x1fc])
			} else {
				d.AsciRx[0] = append(d.AsciRx[0], 255)
			}
			d.CoinCommand = ByteList{}
		} else if (cmd == 100 || cmd == 101) && len(d.CoinCommand) == 2 {
			index := -1
			if v < 16 {
				index = int(v)
			} else {
				index = strings.IndexByte(coinChars, v)
			}
			if index >= 0 {
				d.CoinEnabled[index] = cmd == 101
			}
			d.CoinCommand = ByteList{}
		}
		return
	}
	if v == 100 || v == 101 || v == 113 {
		d.CoinCommand = ByteList{v}
		return
	}
	if v == 103 {
		d.AsciRx[0] = append(d.AsciRx[0], 65) // 'g': wake-up, 'A' (all well)
	} else if v == 114 {
		d.AsciRx[0] = append(d.AsciRx[0], 66, 0, 0) // 'r': 'B', hardware faults, sensor faults
	} else if v == 97 || v == 120 {
		d.CoinAccept = true
	} else if v == 122 || v == 115 {
		d.CoinAccept = false
	} else if v == 99 {
		d.CoinCollected = append(d.CoinCollected, d.CoinEscrow...)
		d.CoinEscrow = []int{}
		m.Event("coin.collected", Ordered())
		d.AsciRx[0] = append(d.AsciRx[0], 65)
	} else if v == 102 {
		d.CoinReturned = append(d.CoinReturned, d.CoinEscrow...)
		d.CoinEscrow = []int{}
		m.Event("coin.returned", Ordered())
		d.AsciRx[0] = append(d.AsciRx[0], 65)
	} else if v == 110 {
		d.AsciRx[0] = append(d.AsciRx[0], 65)
	} else if v == 108 {
		// Learning is mechanical on the real validator. The model has no
		// sensor baselines to learn, and the firmware waits on a timer.
		m.Event("coin.learn-escrow", Ordered())
	} else if v != 107 {
		m.Event("coin.unsupported-command", Ordered("value", int(v)))
	}
}

func dmaDelta(mode int) int {
	if mode == 1 {
		return -1
	}
	if mode == 2 {
		return 0
	}
	return 1
}

// DMA runs one memory-to-memory channel-0 transfer. Unsupported addressing
// modes are reported as events, never silently invented.
func (m *Machine) DMA() {
	p := m.Ports
	src := int(p[0x20]) | int(p[0x21])<<8 | (int(p[0x22])&15)<<16
	dst := int(p[0x23]) | int(p[0x24])<<8 | (int(p[0x25])&15)<<16
	count := int(p[0x26]) | int(p[0x27])<<8
	incS := (int(p[0x31]) >> 2) & 3
	incD := (int(p[0x31]) >> 4) & 3
	if incS == 3 || incD == 3 {
		m.Event("dma.unsupported", Ordered("mode", int(p[0x31])))
		return
	}
	if count == 0 {
		count = 65536
	}
	dS := dmaDelta(incS)
	dD := dmaDelta(incD)
	for i := 0; i < count; i++ {
		m.WritePhysical(dst, m.ReadPhysical(src))
		src = (src + dS) & 0xfffff
		dst = (dst + dD) & 0xfffff
	}
	p[0x20] = byte(src & 255)
	p[0x21] = byte(src >> 8)
	p[0x22] = byte(src >> 16)
	p[0x23] = byte(dst & 255)
	p[0x24] = byte(dst >> 8)
	p[0x25] = byte(dst >> 16)
	p[0x26] = 0
	p[0x27] = 0
	p[0x30] &= ^byte(64)
	m.Event("dma.complete", Ordered("count", count))
}

var timerChannels = [2][3]int{{1, 12, 64}, {2, 20, 128}}

// Tick advances time-dependent device state by t T-states. It runs after
// every instruction and interrupt acceptance, so it is the single place
// where simulated time reaches the peripherals:
//
//   - card reader: drains due magnetic samples into DMA writes and fires
//     insertion/removal completion transitions;
//   - PRT timers: two down-counters clocked every 20 T-states when enabled
//     in port 0x10, reloading from their RC registers and raising their
//     flag bits (0x40/0x80) on underflow;
//   - external interrupt pacing: a 10 ms accumulator (INT2 is level-gated
//     by firmware, this only tracks the pending flag);
//   - telephony power-restore: queues the 0x70 announcement once the
//     scheduled cycle is reached;
//   - scheduled input events due at or before the current cycle.
//
// Errors (e.g. a full telephony queue) propagate to the run loop, which
// converts them into a machine fault — matching the reference behavior
// where such throws abort the current run.
func (m *Machine) Tick(t int) error {
	d := m.Devices
	p := m.Ports
	r := d.Reader
	if r.FinishCycle != 0 || len(r.Samples) > 0 {
		cardTick(m)
	}
	d.TimerAccum += float64(t)
	for d.TimerAccum >= 20 {
		d.TimerAccum -= 20
		if p[16]&3 == 0 {
			continue
		}
		for channel := 0; channel < 2; channel++ {
			spec := timerChannels[channel]
			mask, lo, flag := spec[0], spec[1], spec[2]
			if int(p[16])&mask != 0 {
				count := int(p[lo]) | int(p[lo+1])<<8
				if count == 0 {
					count = int(p[lo+2]) | int(p[lo+3])<<8
					p[16] |= byte(flag)
				} else {
					count--
				}
				p[lo] = byte(count & 255)
				p[lo+1] = byte(count >> 8)
			}
		}
	}
	d.ExternalAccum += float64(t)
	externalPeriod := m.externalPeriod
	if d.ExternalAccum >= externalPeriod {
		d.ExternalAccum -= externalPeriod
		d.ExternalPending = true
	}
	if d.TelephonyResetDue != nil && float64(m.CPU.Cycles) >= *d.TelephonyResetDue {
		d.TelephonyResetDue = nil
		if err := m.QueueIP([]byte{0x70}); err != nil {
			return err
		}
	}
	if len(d.Scheduled) > 0 {
		if err := m.DrainEvents(); err != nil {
			return err
		}
	}
	return nil
}

// ServiceInterrupt offers pending interrupts to the CPU once per executed
// instruction. Vectors are mode-2: the base from port 0x33 bits 5-7 plus a
// fixed offset per source. Priority (first ready source wins, one interrupt
// per call, exactly like the reference):
//
//	+0  MTR2 external coin UART (INT1) when enabled and RX is not idle
//	+2  INT2 — tied active on these boards; firmware gates it via 0x34 bit 2
//	+4/+6  PRT0/PRT1 timer flags (with their enable bits in port 0x10)
//	+8/+10 DMA0/DMA1 completion (when unmasked in port 0x30)
//	+12 CSIO transfer complete
//	+14/+16 ASCI0/ASCI1 receive-ready or transmit-ready with their enables
//
// Acceptance clears the latched device flag only for DMA/CSIO sources;
// timer and serial flags clear through their normal read paths.
func (m *Machine) ServiceInterrupt() error {
	p := m.Ports
	d := m.Devices
	c := &m.CPU
	il := int(p[0x33]) & 0xe0
	if !c.IFF1 || c.EIDelay != 0 {
		return nil
	}
	try := func(vector int) (bool, error) { return c.Interrupt(m, vector, false) }
	if m.Board.Family == "mtr2" && (p[0x34]&2) != 0 && m.CoinUARTInput(0xe2) != 1 {
		_, err := try(il)
		return err
	}
	// INT2 is tied active on this board. Firmware enables it every eight PRT0 ticks.
	if p[0x34]&4 != 0 {
		_, err := try(il + 2)
		return err
	}
	if (p[16] & 0x50) == 0x50 {
		_, err := try(il + 4)
		return err
	}
	if (p[16] & 0xa0) == 0xa0 {
		_, err := try(il + 6)
		return err
	}
	if d.Dma0Interrupt && (p[0x30]&4) != 0 {
		ok, err := try(il + 8)
		if err == nil && ok {
			d.Dma0Interrupt = false
		}
		return err
	}
	if d.Dma1Interrupt && (p[0x30]&8) != 0 {
		ok, err := try(il + 10)
		if err == nil && ok {
			d.Dma1Interrupt = false
		}
		return err
	}
	if d.CsioInterrupt && (p[10]&64) != 0 {
		ok, err := try(il + 12)
		if err == nil && ok {
			d.CsioInterrupt = false
		}
		return err
	}
	cycles := float64(m.CPU.Cycles)
	for ch := 0; ch < 2; ch++ {
		rxReady := len(d.AsciRx[ch]) > 0 && cycles >= d.AsciNextRx[ch] && (int(p[4+ch])&8) != 0
		txReady := (int(p[4+ch])&1) != 0 && cycles >= d.AsciNextTx[ch]
		if rxReady || txReady {
			_, err := try(il + 14 + ch*2)
			return err
		}
	}
	return nil
}

// DeviceEvent injects one external input through the simulated hardware.
// The payload is validated first (ValidateInput), then applied with the
// side effects the real input would have: queued telephony messages,
// scheduled follow-ups (key release, answer-supervision phases), reader
// sample timelines, coin escrow moves. With record=true the input is also
// appended to the replay log (bounded to 10000 entries) and an input.<type>
// event is emitted — exactly the reference behavior, including that a
// failing queue skips both the log entry and the event.
func (m *Machine) DeviceEvent(typ string, data EventData, record bool) error {
	dm := data.Map()
	if err := ValidateInput(typ, dm); err != nil {
		return err
	}
	d := m.Devices
	var qerr error
	queue := func(b []byte) {
		if qerr == nil {
			qerr = m.QueueIP(b)
		}
	}
	switch typ {
	case "handset":
		d.OffHook = dm["offHook"].(bool)
		if d.OffHook {
			queue([]byte{0x62})
		} else {
			queue([]byte{0x60})
		}
	case "key":
		key, _ := dm["key"].(string)
		keys := map[string]int{"1": 0x22, "2": 0x24, "3": 0x26, "4": 0x28, "5": 0x2a,
			"6": 0x2c, "7": 0x2e, "8": 0x30, "9": 0x32, "0": 0x34, "*": 0x36, "#": 0x38,
			"language": 0x5a, "next": 0x58, "volumeUp": 0x56, "volumeDown": 0x54, "release": 0x5e}
		code, ok := keys[key]
		if !ok {
			return errPlain("Unknown key")
		}
		queue([]byte{byte(code)})
		if key != "release" {
			m.Schedule(m.MsCycles(100), "key", NewEventData("key", "release"))
		}
	case "line":
		for _, k := range []string{"lineConnected", "reversed", "ring"} {
			if v, ok := dm[k]; ok {
				b, _ := v.(bool)
				switch k {
				case "lineConnected":
					d.LineConnected = b
				case "reversed":
					d.Reversed = b
				case "ring":
					d.Ring = b
				}
			}
		}
		ModemLine(m, dm)
		if _, ok := dm["lineConnected"]; ok {
			if d.LineConnected {
				queue([]byte{0x66})
			} else {
				queue([]byte{0x64})
			}
		}
		if qerr == nil {
			if _, ok := dm["reversed"]; ok {
				if d.Reversed {
					queue([]byte{0x6a})
				} else {
					queue([]byte{0x68})
				}
			}
		}
	case "answer":
		delay := 3000.0
		if v, ok := dm["delayMs"]; ok && v != nil {
			n, err := Number(v, "delayMs", 0, 60000)
			if err != nil {
				return err
			}
			delay = float64(n)
		}
		method := "both"
		if v, ok := dm["method"].(string); ok && v != "" {
			method = v
		}
		delayCycles := m.MsCycles(delay)
		if method != "tone" {
			m.Schedule(delayCycles, "line", NewEventData("reversed", !d.Reversed))
		}
		if method != "polarity" {
			m.Schedule(delayCycles, "line", NewEventData("answerTone", true))
			m.Schedule(delayCycles+m.MsCycles(3000), "line",
				NewEventData("answerTone", false, "carrier", true))
		}
		m.Event("line.answer.scheduled", Ordered("delayMs", JSNum(delayCycles/(float64(m.Board.ClockHz)/1000)), "method", method))
	case "dtmf":
		codes := "D1234567890*#ABC"
		digitStr, _ := dm["digit"].(string)
		digit := strings.Index(codes, digitStr)
		d.Modem.DTMF = &digit
		dur := 200.0
		if v, ok := dm["durationMs"]; ok && v != nil {
			n, err := Number(v, "durationMs", 20, 10000)
			if err != nil {
				return err
			}
			dur = float64(n)
		}
		d.Modem.DTMFUntil = float64(m.CPU.Cycles) + m.MsCycles(dur)
	case "sensor":
		for _, k := range []string{"coverOpen", "cashboxRemoved"} {
			if v, ok := dm[k]; ok {
				b, _ := v.(bool)
				if k == "coverOpen" {
					d.CoverOpen = b
				} else {
					d.CashboxRemoved = b
				}
			}
		}
	case "telephony":
		b, err := Bytes(dm["hex"])
		if err != nil {
			return err
		}
		queue(b)
	case "serial":
		ch, err := Number(dm["channel"], "channel", 0, 1)
		if err != nil {
			return err
		}
		b, err := Bytes(dm["hex"])
		if err != nil {
			return err
		}
		if len(d.AsciRx[ch])+len(b) > 4096 {
			return errPlain("Serial queue full")
		}
		d.AsciRx[ch] = append(d.AsciRx[ch], b...)
	case "coin":
		index, err := Number(dm["index"], "coin index", 0, 15)
		if err != nil {
			return err
		}
		if d.CoinAccept && d.CoinEnabled[index] {
			d.CoinEscrow = append(d.CoinEscrow, int(index))
			d.AsciRx[0] = append(d.AsciRx[0], coinChars[index])
			m.Event("coin.accepted", Ordered("index", index))
		} else {
			d.CoinReturned = append(d.CoinReturned, int(index))
			d.AsciRx[0] = append(d.AsciRx[0], 48)
			m.Event("coin.rejected", Ordered("index", index))
		}
	case "card":
		if err := CardEvent(m, dm); err != nil {
			return err
		}
	default:
		return errf("Unknown device event: %s", typ)
	}
	if qerr != nil {
		return qerr
	}
	if record {
		m.InputLog = append(m.InputLog, InputLogEntry{
			Cycle: float64(m.CPU.Cycles), Type: typ, Data: data.Clone()})
		if len(m.InputLog) > 10000 {
			m.InputLog = append([]InputLogEntry(nil), m.InputLog[1:]...)
		}
	}
	m.Event("input."+typ, data.Raw)
	return nil
}

// RunResult reports one bounded run: counters, stop reason, debug hit, fault.
type RunResult struct {
	Steps        int             `json:"steps"`
	Instructions int64           `json:"instructions"`
	Cycles       int64           `json:"cycles"`
	Reason       string          `json:"reason"`
	Hit          json.RawMessage `json:"hit"`
	Fault        *Fault          `json:"fault"`
}

// RunOptions parameterizes a bounded run.
type RunOptions struct {
	Instructions      int64
	Cycles            int64 // math.MaxInt64 for Infinity
	IgnoreBreakpoints bool
}

// Run executes up to opts.Instructions instructions and/or opts.Cycles
// T-states (use math.MaxInt64 for an unbounded cycle budget). Each iteration:
// drain due scheduled events, offer interrupts, check breakpoints/trace,
// execute one instruction, check watchpoint hits. Stops early on a
// breakpoint, watchpoint or fault; a non-budget stop also clears Running.
//
// Precondition errors (no ROM loaded) return an error, while execution
// faults are captured in the result:
// Fault carries {message, pc, physical} and a 'fault'
// event is recorded. Breakpoint hits report the matched breakpoint plus
// the current physical PC; watchpoint hits report the access and its value.
func (m *Machine) Run(opts RunOptions) (RunResult, error) {
	if len(m.ROM) == 0 {
		return RunResult{}, errPlain("Load firmware first")
	}
	start := m.CPU.Cycles
	startI := m.CPU.Instructions
	var steps int64
	m.Hit = nil
	m.Fault = nil
	m.StopReason = "budget"

	fault := func(err error) {
		msg := err.Error()
		m.Fault = &Fault{Message: msg, PC: int(m.CPU.PC), Physical: m.Physical(int(m.CPU.PC))}
		m.StopReason = "fault"
		m.Event("fault", Ordered("message", msg, "pc", m.Fault.PC, "physical", m.Fault.Physical))
	}

loop:
	for steps < opts.Instructions && m.CPU.Cycles-start < opts.Cycles {
		if len(m.Devices.Scheduled) > 0 {
			if err := m.DrainEvents(); err != nil {
				fault(err)
				break loop
			}
		}
		if err := m.ServiceInterrupt(); err != nil {
			fault(err)
			break loop
		}
		if (!opts.IgnoreBreakpoints && len(m.Breakpoints) > 0) || m.TraceEnabled {
			pc := int(m.CPU.PC)
			physical := m.Physical(pc)
			if !opts.IgnoreBreakpoints && len(m.Breakpoints) > 0 {
				for _, bp := range m.Breakpoints {
					a := pc
					if bp.Space == "physical" {
						a = physical
					}
					if a == bp.Address && (bp.Bbr == nil || *bp.Bbr == int(m.Ports[0x39])) {
						pairs := []any{"type", "breakpoint", "space", bp.Space, "address", bp.Address}
						if bp.Bbr != nil {
							pairs = append(pairs, "bbr", *bp.Bbr)
						}
						pairs = append(pairs, "physical", physical)
						m.Hit = Ordered(pairs...)
						m.StopReason = "breakpoint"
						break loop
					}
				}
			}
			if m.TraceEnabled {
				raw := make([]byte, 4)
				for i := 0; i < 4; i++ {
					raw[i] = byte(m.ReadPhysical((physical + i) & 0xfffff))
				}
				m.Trace = append(m.Trace, TraceRow{PC: pc, Physical: physical,
					Bbr: int(m.Ports[0x39]), Cycle: m.CPU.Cycles, Bytes: hexEncode(raw),
					A: int(m.CPU.A), F: int(m.CPU.F), SP: int(m.CPU.SP)})
				if len(m.Trace) > m.TraceLimit {
					m.Trace = append([]TraceRow(nil), m.Trace[1:]...)
				}
			}
		}
		if _, err := m.CPU.Step(m); err != nil {
			fault(err)
			break loop
		}
		steps++
		if m.Hit != nil {
			m.StopReason = "watchpoint"
			break loop
		}
	}
	if m.StopReason != "budget" {
		m.Running = false
	}
	return RunResult{Steps: int(steps), Instructions: m.CPU.Instructions - startI,
		Cycles: m.CPU.Cycles - start, Reason: m.StopReason, Hit: m.Hit, Fault: m.Fault}, nil
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&15]
	}
	return string(out)
}

var mapLineRe = regexp.MustCompile(`(?i)^([0-9a-f]{8})\s+([0-9a-f]{8})\s+(\S+)\s+(\S+)\s+\(([^)]+)\)`)

// ParseMap reads the companion .MAP symbol file format: eight hex digits of
// physical address, eight of logical address, symbol name, segment and
// module. Physical comes first in each row.
func ParseMap(text string) []Symbol {
	symbols := []Symbol{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if mm := mapLineRe.FindStringSubmatch(line); mm != nil {
			logical, _ := strconv.ParseInt(mm[2], 16, 64)
			physical, _ := strconv.ParseInt(mm[1], 16, 64)
			symbols = append(symbols, Symbol{Address: int(logical), Physical: int(physical),
				Name: mm[3], Segment: mm[4], Module: mm[5]})
		}
	}
	return symbols
}

// ValidateInput checks a device-event payload before it is applied; the
// error messages are part of the API contract.
func ValidateInput(typ string, data map[string]any) error {
	if data == nil {
		return errPlain("Device data must be an object")
	}
	switch typ {
	case "handset":
		if _, ok := data["offHook"].(bool); !ok {
			return errPlain("offHook must be boolean")
		}
	case "key":
		valid := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "#",
			"language", "next", "volumeUp", "volumeDown", "release"}
		k, ok := data["key"].(string)
		if !ok {
			return errPlain("Unknown key")
		}
		found := false
		for _, v := range valid {
			if k == v {
				found = true
				break
			}
		}
		if !found {
			return errPlain("Unknown key")
		}
	case "line", "sensor":
		keys := []string{"coverOpen", "cashboxRemoved"}
		if typ == "line" {
			keys = []string{"lineConnected", "reversed", "ring", "dialTone", "busy", "answerTone", "carrier"}
		}
		for _, k := range keys {
			if v, ok := data[k]; ok {
				if _, isBool := v.(bool); !isBool {
					return errf("%s must be boolean", k)
				}
			}
		}
	case "answer":
		dv := data["delayMs"]
		if dv == nil {
			dv = float64(3000)
		}
		if _, err := Number(dv, "delayMs", 0, 60000); err != nil {
			return err
		}
		method := "both"
		if v, ok := data["method"].(string); ok && v != "" {
			method = v
		} else if v, ok := data["method"]; ok && v != nil {
			return errPlain("Answer method must be polarity, tone or both")
		}
		if method != "polarity" && method != "tone" && method != "both" {
			return errPlain("Answer method must be polarity, tone or both")
		}
	case "dtmf":
		digit, ok := data["digit"].(string)
		if !ok || len(digit) != 1 || !strings.Contains("D1234567890*#ABC", digit) {
			return errPlain("Invalid DTMF digit")
		}
		dv := data["durationMs"]
		if dv == nil {
			dv = float64(200)
		}
		if _, err := Number(dv, "durationMs", 20, 10000); err != nil {
			return err
		}
	case "card":
		return ValidateCard(data)
	case "coin":
		_, err := Number(data["index"], "coin index", 0, 15)
		return err
	case "telephony", "serial":
		b, err := Bytes(data["hex"])
		if err != nil {
			return err
		}
		if len(b) > 4096 {
			return errPlain("Device byte payload too large")
		}
		if typ == "serial" {
			_, err := Number(data["channel"], "channel", 0, 1)
			return err
		}
		return nil
	default:
		return errf("Unknown device event: %s", typ)
	}
	return nil
}
