package emu

// SSI modem serial control/status bus (PIO A), plus observable simulated
// line signals. This allows firmware to perform its own dial-tone, answer
// and carrier debounce tests rather than forcing firmware result flags.

type Modem struct {
	Controls       ByteList `json:"controls"`
	WriteBits      []int    `json:"writeBits"`
	ReadValue      int      `json:"readValue"`
	ReadIndex      int      `json:"readIndex"`
	DialTone       bool     `json:"dialTone"`
	Busy           bool     `json:"busy"`
	AnswerTone     bool     `json:"answerTone"`
	Carrier        bool     `json:"carrier"`
	DTMF           *int     `json:"dtmf"`
	DTMFUntil      float64  `json:"dtmfUntil"`
	Digits         string   `json:"digits"`
	LastDigitCycle float64  `json:"lastDigitCycle"`
	CallGeneration int      `json:"callGeneration"`
}

func modemState() *Modem {
	return &Modem{Controls: make(ByteList, 4), WriteBits: []int{}, DialTone: true, Digits: ""}
}

const modemDigitChars = "D1234567890*#ABC"

func modemRelays(m *Machine, value, old int) {
	mod := m.Devices.Modem
	// Relay 1 connects the modem. Relay 2 HIGH isolates the telephony circuit
	// from the CO line (supplementary power). Thus ...10 is a released line;
	// ...10 -> ...11 is the firmware seizing the line for a fresh outbound
	// call, which starts a new call generation.
	released := (value&3) == 2 && (old&3) != 2
	newCall := (value&1) != 0 && (old&1) == 0 && (old&2) != 0
	if released || newCall {
		mod.Digits = ""
		mod.LastDigitCycle = 0
		mod.AnswerTone = false
		mod.Carrier = false
		mod.ReadValue = 0
		mod.ReadIndex = 0
		m.Devices.Dialed = ""
		if newCall {
			mod.CallGeneration++
			m.Event("modem.call-start", Ordered("callGeneration", mod.CallGeneration))
		} else {
			m.Event("modem.line-released", Ordered())
		}
	}
}

func detectRegister(m *Machine) int {
	mod := m.Devices.Modem
	if !m.Devices.LineConnected || (m.RelayState()&1) == 0 {
		return 0
	}
	var progress bool
	if mod.Busy {
		progress = int(float64(m.CPU.Cycles)/m.MsCycles(500))%2 != 0
	} else {
		progress = mod.DialTone && mod.Digits == ""
	}
	v := boolInt(progress, 2) | boolInt(mod.AnswerTone, 4)
	if mod.Carrier {
		v |= 0x28
	}
	return v
}

func modemOutput(m *Machine, value, old int) {
	mod := m.Devices.Modem
	address := (value >> 4) & 3
	if m.Board.Family == "mtr2" {
		address = (value >> 4) & 7
	}
	if (value&4) == 0 && (old&4) != 0 {
		if m.Board.Family == "mtr2" {
			mod.ReadIndex = -1
		} else {
			mod.ReadIndex = 0
		}
		if address == 2 {
			mod.ReadValue = detectRegister(m)
		} else if address < len(mod.Controls) {
			mod.ReadValue = int(mod.Controls[address])
		} else {
			mod.ReadValue = 0
		}
	} else if (value&4) == 0 && (value&2) == 0 && (old&2) != 0 {
		mod.ReadIndex++
	}
	if (value&4) != 0 && (value&2) == 0 && (old&2) != 0 {
		bit := 1
		if (value & 1) != 0 {
			bit = 0
		}
		mod.WriteBits = append(mod.WriteBits, bit)
		if len(mod.WriteBits) > 8 {
			mod.WriteBits = mod.WriteBits[1:]
		}
	}
	if (value&8) != 0 && (old&8) == 0 && len(mod.WriteBits) == 8 {
		data := 0
		for i, bit := range mod.WriteBits {
			data |= bit << i
		}
		// JS arrays grow on assignment; MTR2 addresses 4..7 extend controls.
		for len(mod.Controls) <= address {
			mod.Controls = append(mod.Controls, 0)
		}
		previous := int(mod.Controls[address])
		mod.Controls[address] = byte(data)
		mod.WriteBits = []int{}
		if address == 3 && (data&16) != 0 && (previous&16) == 0 {
			digit := string(modemDigitChars[data&15])
			mod.Digits += digit
			mod.LastDigitCycle = float64(m.CPU.Cycles)
			m.Devices.Dialed = mod.Digits
			m.Event("modem.dial", Ordered("digit", digit, "number", mod.Digits))
		}
	}
}

func modemStatusBit(m *Machine) int {
	mod := m.Devices.Modem
	if int(m.Ports[m.Board.PioA])&4 == 0 && mod.ReadIndex >= 0 {
		idx := mod.ReadIndex
		if idx > 7 {
			idx = 7
		}
		return (mod.ReadValue >> idx) & 1
	}
	return 0
}

// ModemLine applies dialTone/busy/answerTone/carrier keys present in data.
func ModemLine(m *Machine, data map[string]any) {
	mod := m.Devices.Modem
	for _, key := range []struct {
		name string
		dst  *bool
	}{{"dialTone", &mod.DialTone}, {"busy", &mod.Busy}, {"answerTone", &mod.AnswerTone}, {"carrier", &mod.Carrier}} {
		if v, ok := data[key.name]; ok {
			if b, ok := v.(bool); ok {
				*key.dst = b
			}
		}
	}
}
