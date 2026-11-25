package emu

import (
	"encoding/json"
	"sort"
	"strconv"
)

// Millennium's 2 x 20 character VFD, driven by a byte-oriented command/data
// bus (a PIO B bit selects command vs data mode for writes to the display
// port). The parser is a small state machine:
//
//   - command mode: 0x40 resets the whole display state (revision counter
//     survives, incremented); values < 40 set the cursor address.
//   - data mode: control bytes (< 0x20) switch scroll mode (0x11 normal
//     wrap, 0x12 vertical, 0x15 row-local horizontal), toggle cursor
//     visibility (0x13/0x14), clear (0x0c), or arm a one-byte parameter
//     (0x16 blink position, 0x1a intensity, 0x1b five-byte custom font);
//     printable bytes are written at the cursor per the current mode.
//
// Scroll behavior is driven purely by firmware writes — there is no wall-
// clock marquee. In horizontal mode the first write at a row margin
// overwrites that cell and arms scrollPending; subsequent writes shift that
// row only (the two-character previous/current pattern firmware ad scrollers
// and keypad echo use). In vertical mode an overflow write shifts the bottom
// row up and clears it. Parameter and font bytes are consumed by the pending
// state first, so they can never be misinterpreted as control codes.

const (
	VFDWidth      = 20
	VFDSize       = 40
	VFDNormal     = 0x11
	VFDVertical   = 0x12
	VFDHorizontal = 0x15
)

// CustomChars marshals integer keys as quoted strings in ascending
// numeric order, matching JS object key iteration in snapshots.
type CustomChars map[int]ByteList

func (cc CustomChars) MarshalJSON() ([]byte, error) {
	keys := make([]int, 0, len(cc))
	for k := range cc {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := []byte("{")
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, []byte(strconv.Quote(strconv.Itoa(k))+":")...)
		v, _ := json.Marshal(cc[k])
		out = append(out, v...)
	}
	return append(out, '}'), nil
}

func (cc *CustomChars) UnmarshalJSON(data []byte) error {
	var raw map[string]ByteList
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m := CustomChars{}
	for k, v := range raw {
		n, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		m[n] = v
	}
	*cc = m
	return nil
}

// VFDState is the display parser state. It embeds into Devices, so its
// field order defines the leading keys of the devices JSON.
type VFDState struct {
	Display                 ByteList    `json:"display"`
	Cursor                  int         `json:"cursor"`
	DisplayMode             int         `json:"displayMode"`
	DisplayCursorVisible    bool        `json:"displayCursorVisible"`
	DisplayScrollPending    bool        `json:"displayScrollPending"`
	DisplayBlinkPositions   []int       `json:"displayBlinkPositions"`
	DisplayIntensity        int         `json:"displayIntensity"`
	DisplayRevision         int64       `json:"displayRevision"`
	DisplayPending          int         `json:"displayPending"`
	CustomRemaining         int         `json:"customRemaining"`
	DisplayCustomCode       *int        `json:"displayCustomCode"`
	DisplayCustomData       ByteList    `json:"displayCustomData"`
	DisplayCustomCharacters CustomChars `json:"displayCustomCharacters"`
}

func newVFDState() VFDState {
	d := make(ByteList, VFDSize)
	for i := range d {
		d[i] = 32
	}
	return VFDState{Display: d, DisplayMode: VFDNormal, DisplayBlinkPositions: []int{},
		DisplayCustomData: ByteList{}, DisplayCustomCharacters: CustomChars{}}
}

func resetVFD(d *VFDState) {
	rev := d.DisplayRevision + 1
	*d = newVFDState()
	d.DisplayRevision = rev
}

func clearVFD(d *VFDState) {
	for i := range d.Display {
		d.Display[i] = 32
	}
	d.Cursor = 0
	d.DisplayScrollPending = false
}

// WriteVFD feeds one byte to the display: in command mode it handles reset
// and cursor addressing; in data mode it runs the control/print parser
// described above. Unknown command bytes are reported as events.
func WriteVFD(m *Machine, d *VFDState, value int, command bool) {
	v := value & 255
	d.DisplayRevision++
	if command {
		if v == 0x40 {
			resetVFD(d)
		} else if v < VFDSize {
			d.Cursor = v
			d.DisplayScrollPending = false
		} else {
			m.Event("display.unknown-command", Ordered("value", v))
		}
		return
	}

	// Parameter/font bytes must never be interpreted as control characters.
	if d.CustomRemaining != 0 {
		d.DisplayCustomData = append(d.DisplayCustomData, byte(v))
		d.CustomRemaining--
		if d.CustomRemaining == 0 {
			if d.DisplayCustomCode != nil && len(d.DisplayCustomData) == 5 {
				cp := append(ByteList(nil), d.DisplayCustomData...)
				d.DisplayCustomCharacters[*d.DisplayCustomCode] = cp
			}
			d.DisplayCustomCode = nil
			d.DisplayCustomData = ByteList{}
		}
		return
	}
	if d.DisplayPending != 0 {
		pending := d.DisplayPending
		d.DisplayPending = 0
		if pending == 0x16 {
			if v < VFDSize && !containsInt(d.DisplayBlinkPositions, v) {
				d.DisplayBlinkPositions = append(d.DisplayBlinkPositions, v)
			}
		} else if pending == 0x1a {
			d.DisplayIntensity = v
		} else if pending == 0x1b {
			code := v
			d.DisplayCustomCode = &code
			d.DisplayCustomData = ByteList{}
			d.CustomRemaining = 5
		}
		return
	}
	switch v {
	case 0x0c:
		clearVFD(d)
		return
	case VFDNormal, VFDVertical, VFDHorizontal:
		if d.DisplayMode != v {
			d.DisplayScrollPending = false
		}
		d.DisplayMode = v
		return
	case 0x13:
		d.DisplayCursorVisible = true
		return
	case 0x14:
		d.DisplayCursorVisible = false
		return
	case 0x16, 0x1a, 0x1b:
		d.DisplayPending = v
		return
	case 0x17:
		d.DisplayBlinkPositions = []int{}
		return
	}
	if v < 32 {
		return
	}

	if d.DisplayMode == VFDHorizontal {
		start := 0
		if d.Cursor >= VFDWidth {
			start = VFDWidth
		}
		if d.DisplayScrollPending {
			copy(d.Display[start:start+VFDWidth], d.Display[start+1:start+VFDWidth])
			d.Display[start+VFDWidth-1] = byte(v)
		} else {
			d.Display[d.Cursor] = byte(v)
			if d.Cursor-start == VFDWidth-1 {
				d.DisplayScrollPending = true
			} else {
				d.Cursor++
			}
		}
		return
	}

	if d.DisplayMode == VFDVertical {
		if d.DisplayScrollPending {
			copy(d.Display[0:VFDWidth], d.Display[VFDWidth:VFDSize])
			for i := VFDWidth; i < VFDSize; i++ {
				d.Display[i] = 32
			}
			d.Cursor = VFDWidth
			d.DisplayScrollPending = false
		}
		d.Display[d.Cursor] = byte(v)
		if d.Cursor == VFDSize-1 {
			d.DisplayScrollPending = true
		} else {
			d.Cursor++
		}
		return
	}

	d.Display[d.Cursor] = byte(v)
	d.Cursor = (d.Cursor + 1) % VFDSize
	d.DisplayScrollPending = false
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
