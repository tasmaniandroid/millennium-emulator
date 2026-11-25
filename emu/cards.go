package emu

import (
	"encoding/hex"
	"regexp"
)

// Physical card reader model:
//
//   - Magnetic stripe: Track 2 (F2F) data is encoded into per-bit samples;
//     each sample arrives on a fixed simulated-time period and triggers one
//     DMA request into firmware buffers, with CP/CFS position sensors read
//     through the status port.
//   - Smart card: the synchronous GPM103-style contact interface. Firmware
//     drives a clock/reset/write line triplet; reads return one image bit at
//     a time, and writes debit the five counter stages with write-carry
//     semantics. Issuer/manufacturer bytes are immutable in this model.

type Reader struct {
	Kind             *string  `json:"kind"`
	Position         string   `json:"position"`
	Track2           string   `json:"track2"`
	Image            ByteList `json:"image"`
	WriteProtected   bool     `json:"writeProtected"`
	Samples          ByteList `json:"samples"`
	NextSample       float64  `json:"nextSample"`
	SamplePeriod     float64  `json:"samplePeriod"`
	FinishCycle      float64  `json:"finishCycle"`
	FinishPosition   string   `json:"finishPosition"`
	Address          int      `json:"address"`
	ResetClocked     bool     `json:"resetClocked"`
	WriteArmed       bool     `json:"writeArmed"`
	WriteStart       *int64   `json:"writeStart"`
	LastWriteAddress int      `json:"lastWriteAddress"`
	WriteCount       int      `json:"writeCount"`
	Reads            int      `json:"reads"`
	Writes           int      `json:"writes"`
	DmaSamples       int      `json:"dmaSamples"`
}

func readerState(previous *Reader) *Reader {
	r := &Reader{Position: "out", Track2: "", Image: ByteList{}, Samples: ByteList{},
		SamplePeriod: 3072, FinishPosition: "out", LastWriteAddress: -1}
	if previous != nil {
		r.Kind = previous.Kind
		r.Position = previous.Position
		r.Track2 = previous.Track2
		if previous.Image != nil {
			r.Image = append(ByteList(nil), previous.Image...)
		}
		r.WriteProtected = previous.WriteProtected
	}
	return r
}

var track2Re = regexp.MustCompile(`^;[0-9=]{1,37}\?$`)

// EncodeTrack2 encodes Track 2 (F2F): ';'/'?' sentinels, LRC character,
// five bits per character LSB-first with odd parity, twelve zero bits of
// lead-in and trail.
func EncodeTrack2(track string) ([]int, error) {
	if len(track) == 0 || track[0] != ';' {
		track = ";" + track
	}
	if track[len(track)-1] != '?' {
		track += "?"
	}
	if !track2Re.MatchString(track) {
		return nil, errPlain("Track 2 must contain 1..37 digits/separators between ; and ?")
	}
	codes := make([]int, 0, len(track)+1)
	lrc := 0
	for i := 0; i < len(track); i++ {
		var c int
		switch track[i] {
		case ';':
			c = 11
		case '=':
			c = 13
		case '?':
			c = 15
		default:
			c = int(track[i] - '0')
		}
		codes = append(codes, c)
		lrc ^= c
	}
	codes = append(codes, lrc)
	bits := make([]int, 0, 12+len(codes)*5+12)
	for i := 0; i < 12; i++ {
		bits = append(bits, 0)
	}
	for _, nibble := range codes {
		ones := 0
		for i := 0; i < 4; i++ {
			bit := (nibble >> i) & 1
			bits = append(bits, bit)
			ones += bit
		}
		if ones%2 != 0 {
			bits = append(bits, 0)
		} else {
			bits = append(bits, 1)
		}
	}
	for i := 0; i < 12; i++ {
		bits = append(bits, 0)
	}
	return bits, nil
}

func cardSensors(r *Reader) int {
	switch r.Position {
	case "seated":
		return 6
	case "partial":
		return 2
	}
	return 0
}

func cardInput(m *Machine) int {
	r := m.Devices.Reader
	if r.Kind == nil || *r.Kind != "smart" || r.Position != "seated" || len(r.Image) == 0 {
		return 1
	}
	r.Reads++
	idx := r.Address >> 3
	v := 255
	if idx < len(r.Image) {
		v = int(r.Image[idx])
	}
	return (v >> (7 - (r.Address & 7))) & 1
}

func smartOutput(m *Machine, port, value, old int) {
	r := m.Devices.Reader
	if r.Kind == nil || *r.Kind != "smart" || r.Position != "seated" {
		return
	}
	if m.Board.Family == "mtr2" {
		if port == 0xc2 && (value&8) != 0 && (old&8) == 0 {
			r.Address = 0
			r.WriteArmed = false
			r.WriteStart = nil
			r.LastWriteAddress = -1
		}
		if port != 0xc1 && port != 0xc2 {
			return
		}
		d := int(m.Ports[0xc1])
		e := int(m.Ports[0xc2])
		previousD, previousE := d, e
		if port == 0xc1 {
			previousD = old
		}
		if port == 0xc2 {
			previousE = old
		}
		value = boolInt((d&8) != 0, 64) | boolInt((e&4) != 0, 128)
		old = boolInt((previousD&8) != 0, 64) | boolInt((previousE&4) != 0, 128)
		port = 0x42
	}
	if port == 0x40 && (value&64) != 0 && (old&64) == 0 {
		r.Address = 0
		r.WriteArmed = false
		r.WriteStart = nil
		r.LastWriteAddress = -1
	}
	if port != 0x42 {
		return
	}
	clock := (value & 64) != 0
	oldClock := (old & 64) != 0
	reset := (value & 128) != 0
	oldReset := (old & 128) != 0
	if !reset && oldReset {
		r.ResetClocked = false
	}
	if !reset && !clock && oldClock {
		r.Address = 0
		r.ResetClocked = true
		r.WriteArmed = false
		r.LastWriteAddress = -1
	}
	if reset && !oldReset {
		r.WriteArmed = !r.ResetClocked
	}
	if !clock && oldClock && reset && r.WriteArmed {
		cy := m.CPU.Cycles
		r.WriteStart = &cy
		r.WriteArmed = false
	}
	if clock && !oldClock && reset {
		if r.WriteStart != nil {
			by := r.Address >> 3
			mask := 1 << (7 - (r.Address & 7))
			// A short clock pulse is not a programming pulse. Issuer/manufacturer
			// bytes are immutable; only the five counter stages can be debited.
			if float64(m.CPU.Cycles-*r.WriteStart) >= m.MsCycles(1024.0/6144) && !r.WriteProtected && by >= 8 && by < 13 {
				if r.LastWriteAddress == r.Address {
					// Repeating a debit at the same address carries into the next stage.
					if by+1 < 13 {
						r.Image[by+1] = 255
					}
				} else {
					r.Image[by] &= byte(^mask & 0xff)
				}
				r.LastWriteAddress = r.Address
				r.Writes++
				r.WriteCount++
				m.Event("smart.write", Ordered("address", r.Address, "value", int(r.Image[by]), "carry", r.WriteCount > 1))
			}
			r.WriteStart = nil
		} else {
			r.Address = (r.Address + 1) % 256
			r.LastWriteAddress = -1
			r.WriteCount = 0
		}
	}
}

// CardEvent applies a card action: insert (magnetic or smart), remove, or
// a direct position change. Inserts require an empty reader; removal keeps
// the card 'partial' until its sample timeline completes.
func CardEvent(m *Machine, data map[string]any) error {
	r := m.Devices.Reader
	action, _ := data["action"].(string)
	if action == "" {
		if v, ok := data["action"]; ok && v != nil {
			return errPlain("Card action must be insert, remove or position")
		}
		action = "insert"
	}
	switch action {
	case "position":
		pos, _ := data["position"].(string)
		r.Position = pos
		return nil
	case "remove":
		r.Position = "partial"
		if r.Kind != nil && *r.Kind == "magnetic" {
			bits, err := EncodeTrack2(r.Track2)
			if err != nil {
				return err
			}
			rev := make(ByteList, len(bits))
			for i, b := range bits {
				rev[len(bits)-1-i] = byte(b)
			}
			r.Samples = rev
		} else {
			r.Samples = ByteList{}
		}
		r.NextSample = float64(m.CPU.Cycles) + m.MsCycles(150)
		r.FinishCycle = r.NextSample + float64(len(r.Samples))*r.SamplePeriod + m.MsCycles(100)
		r.FinishPosition = "out"
		return nil
	case "insert":
	default:
		return errPlain("Card action must be insert, remove or position")
	}
	if r.Position != "out" {
		return errPlain("Remove the current card before inserting another")
	}
	kept := readerState(nil)
	*r = *kept
	r.SamplePeriod = m.MsCycles(0.5)
	kind := "magnetic"
	if v, ok := data["kind"].(string); ok && v != "" {
		kind = v
	} else if v, ok := data["kind"]; ok && v != nil {
		kind = ""
	}
	r.Kind = &kind
	r.Position = "partial"
	if kind == "magnetic" {
		t2, _ := data["track2"].(string)
		r.Track2 = t2
		bits, err := EncodeTrack2(t2)
		if err != nil {
			return err
		}
		r.Samples = make(ByteList, len(bits))
		for i, b := range bits {
			r.Samples[i] = byte(b)
		}
	} else {
		hexStr, _ := data["hex"].(string)
		clean := wsRe.ReplaceAllString(hexStr, "")
		img, err := hex.DecodeString(clean)
		if err != nil {
			return errPlain("Smart card image must contain 13..32 hexadecimal bytes")
		}
		r.Image = ByteList(img)
		r.WriteProtected = data["writeProtected"] == true
	}
	// Give firmware time to see CP before data.
	r.NextSample = float64(m.CPU.Cycles) + m.MsCycles(150)
	r.FinishCycle = r.NextSample + float64(len(r.Samples))*r.SamplePeriod + m.MsCycles(100)
	r.FinishPosition = "seated"
	return nil
}

func cardTick(m *Machine) {
	r := m.Devices.Reader
	p := m.Ports
	for len(r.Samples) > 0 && float64(m.CPU.Cycles) >= r.NextSample {
		bit := int(r.Samples[0])
		r.Samples = r.Samples[1:]
		r.NextSample += r.SamplePeriod
		ch := m.Board.CardDMA
		enable := 64
		lo := 0x26
		addr := 0x23
		if ch != 0 {
			enable = 128
			lo = 0x2e
			addr = 0x28
		}
		if (p[0x30]&byte(enable)) == 0 || (ch == 0 && (p[0x31]&12) != 12) {
			continue
		}
		count := int(p[lo]) | int(p[lo+1])<<8
		if count == 0 {
			count = 65536
		}
		dest := int(p[addr]) | int(p[addr+1])<<8 | (int(p[addr+2])&15)<<16
		m.WritePhysical(dest, ((m.Input(m.Board.Status1) & 254) | bit))
		mode := 0
		if ch == 0 {
			mode = (int(p[0x31]) >> 4) & 3
		}
		delta := 1
		if mode == 1 {
			delta = -1
		} else if mode == 2 {
			delta = 0
		}
		next := (dest + delta) & 0xfffff
		p[addr] = byte(next & 255)
		p[addr+1] = byte(next >> 8 & 255)
		p[addr+2] = byte(next >> 16)
		count--
		p[lo] = byte(count & 255)
		p[lo+1] = byte(count >> 8)
		r.DmaSamples++
		if count == 0 {
			p[0x30] &= byte(^enable & 0xff)
			if ch != 0 {
				m.Devices.Dma1Interrupt = (p[0x30] & 8) != 0
			} else {
				m.Devices.Dma0Interrupt = (p[0x30] & 4) != 0
			}
		}
	}
	if r.FinishCycle != 0 && float64(m.CPU.Cycles) >= r.FinishCycle {
		r.Position = r.FinishPosition
		r.FinishCycle = 0
		m.Event("card.position", Ordered("kind", kindOrNull(r), "position", r.Position, "dmaSamples", r.DmaSamples))
	}
}

func kindOrNull(r *Reader) any {
	if r.Kind == nil {
		return nil
	}
	return *r.Kind
}

var smartHexRe = regexp.MustCompile(`(?i)^(?:[0-9a-f]{2}){13,32}$`)

// ValidateCard checks a card device-event payload; error messages are part
// of the API contract.
func ValidateCard(data map[string]any) error {
	action, _ := data["action"].(string)
	if action == "" {
		if v, ok := data["action"]; ok && v != nil {
			return errPlain("Card action must be insert, remove or position")
		}
		action = "insert"
	}
	switch action {
	case "remove":
		return nil
	case "position":
		pos, ok := data["position"].(string)
		if !ok || (pos != "out" && pos != "partial" && pos != "seated") {
			return errPlain("position must be out, partial or seated")
		}
		return nil
	case "insert":
	default:
		return errPlain("Card action must be insert, remove or position")
	}
	kind := "magnetic"
	if v, ok := data["kind"].(string); ok {
		kind = v
	} else if _, ok := data["kind"]; ok && data["kind"] != nil {
		return errPlain("Card kind must be magnetic or smart")
	}
	if kind == "magnetic" {
		t2, ok := data["track2"].(string)
		if !ok {
			return errPlain("track2 must be a string")
		}
		_, err := EncodeTrack2(t2)
		return err
	}
	if kind == "smart" {
		hexStr, ok := data["hex"].(string)
		if !ok || !smartHexRe.MatchString(wsRe.ReplaceAllString(hexStr, "")) {
			return errPlain("Smart card image must contain 13..32 hexadecimal bytes")
		}
		if wp, ok := data["writeProtected"]; ok && wp != nil {
			if _, isBool := wp.(bool); !isBool {
				return errPlain("writeProtected must be boolean")
			}
		}
		return nil
	}
	return errPlain("Card kind must be magnetic or smart")
}
