package emu

import "fmt"

type FirmwareInfo struct {
	Path           string `json:"path"`
	Size           int    `json:"size"`
	Sha256         string `json:"sha256"`
	OriginalSha256 string `json:"originalSha256"`
}

type BoardState struct {
	ID          string `json:"id"`
	Family      string `json:"family"`
	ClockHz     int    `json:"clockHz"`
	FlashOffset *int   `json:"flashOffset"`
}

type MmuState struct {
	Cbr  int `json:"cbr"`
	Bbr  int `json:"bbr"`
	Cbar int `json:"cbar"`
}

type StateReader struct {
	Kind       *string `json:"kind"`
	Position   string  `json:"position"`
	Address    int     `json:"address"`
	Reads      int     `json:"reads"`
	Writes     int     `json:"writes"`
	DmaSamples int     `json:"dmaSamples"`
}

type StateModem struct {
	DialTone   bool   `json:"dialTone"`
	Busy       bool   `json:"busy"`
	AnswerTone bool   `json:"answerTone"`
	Carrier    bool   `json:"carrier"`
	Digits     string `json:"digits"`
}

type StateDevices struct {
	OffHook         bool        `json:"offHook"`
	LineConnected   bool        `json:"lineConnected"`
	Reversed        bool        `json:"reversed"`
	Ring            bool        `json:"ring"`
	CoverOpen       bool        `json:"coverOpen"`
	CashboxRemoved  bool        `json:"cashboxRemoved"`
	Dialed          string      `json:"dialed"`
	CallState       int         `json:"callState"`
	Relays          int         `json:"relays"`
	CoinAccept      bool        `json:"coinAccept"`
	CoinEscrow      []int       `json:"coinEscrow"`
	CoinCollected   int         `json:"coinCollected"`
	CoinReturned    int         `json:"coinReturned"`
	Management      string      `json:"management"`
	TelephonyQueued int         `json:"telephonyQueued"`
	UnknownCommands ByteList    `json:"unknownCommands"`
	Reader          StateReader `json:"reader"`
	Modem           StateModem  `json:"modem"`
}

type Capabilities struct {
	Cpu        string `json:"cpu"`
	Telephony  string `json:"telephony"`
	Eeprom     string `json:"eeprom"`
	Coin       string `json:"coin"`
	Card       string `json:"card"`
	Management string `json:"management"`
	Audio      string `json:"audio"`
}

type MachineState struct {
	Firmware        *FirmwareInfo   `json:"firmware"`
	Board           BoardState      `json:"board"`
	Running         bool            `json:"running"`
	StopReason      string          `json:"stopReason"`
	Fault           *Fault          `json:"fault"`
	Cpu             CPUState        `json:"cpu"`
	PhysicalPC      int             `json:"physicalPC"`
	Milliseconds    float64         `json:"milliseconds"`
	Mmu             MmuState        `json:"mmu"`
	Display         [2]string       `json:"display"`
	DisplayHardware DisplayHardware `json:"displayHardware"`
	Devices         StateDevices    `json:"devices"`
	Capabilities    Capabilities    `json:"capabilities"`
	Breakpoints     []Breakpoint    `json:"breakpoints"`
	Watchpoints     []Watchpoint    `json:"watchpoints"`
	Patches         int             `json:"patches"`
	EventID         int64           `json:"eventId"`
}

// State builds the full machine-state payload served by /api/state and
// embedded in most mutation responses.
func (m *Machine) State() MachineState {
	d := m.Devices
	pc := int(m.CPU.PC)
	st := MachineState{
		Board:        BoardState{ID: m.Board.ID, Family: m.Board.Family, ClockHz: m.Board.ClockHz},
		Running:      m.Running,
		StopReason:   m.StopReason,
		Fault:        m.Fault,
		Cpu:          m.GetCPUState(),
		PhysicalPC:   m.Physical(pc),
		Milliseconds: float64(m.CPU.Cycles) / (float64(m.Board.ClockHz) / 1000),
		Mmu: MmuState{Cbr: int(m.Ports[0x38]), Bbr: int(m.Ports[0x39]),
			Cbar: int(m.Ports[0x3a])},
		Devices: StateDevices{
			OffHook: d.OffHook, LineConnected: d.LineConnected, Reversed: d.Reversed,
			Ring: d.Ring, CoverOpen: d.CoverOpen, CashboxRemoved: d.CashboxRemoved,
			Dialed: d.Dialed, CallState: d.CallState, Relays: m.RelayState(),
			CoinAccept: d.CoinAccept, CoinEscrow: d.CoinEscrow,
			CoinCollected: len(d.CoinCollected), CoinReturned: len(d.CoinReturned),
			Management: d.Management, TelephonyQueued: len(d.IpQueue),
			UnknownCommands: d.UnknownCommands,
			Reader: StateReader{Kind: d.Reader.Kind, Position: d.Reader.Position,
				Address: d.Reader.Address, Reads: d.Reader.Reads,
				Writes: d.Reader.Writes, DmaSamples: d.Reader.DmaSamples},
			Modem: StateModem{DialTone: d.Modem.DialTone, Busy: d.Modem.Busy,
				AnswerTone: d.Modem.AnswerTone, Carrier: d.Modem.Carrier,
				Digits: d.Modem.Digits},
		},
		Breakpoints: m.Breakpoints,
		Watchpoints: m.Watchpoints,
		Patches:     len(m.Patches),
		EventID:     m.EventID,
	}
	if m.Filename != "" {
		st.Firmware = &FirmwareInfo{Path: m.Filename, Size: len(m.ROM),
			Sha256: Hash(m.ROM), OriginalSha256: Hash(m.Original)}
	}
	if m.Board.Family == "mtr2" {
		off := m.FlashOffset(m.Physical(pc))
		if off >= 0 {
			st.Board.FlashOffset = &off
		}
	}
	ds := m.DisplayState()
	st.Display = ds.Display
	st.DisplayHardware = ds.DisplayHardware

	eeprom := "93C46 64x16"
	coin := "basic validator and escrow model"
	if m.Board.Family == "mtr2" {
		eeprom = "93C66 256x16"
		coin = "external UART / INT1; basic validator and escrow model"
	}
	st.Capabilities = Capabilities{
		Cpu:        "Z80 + Z180 extensions; approximate timing",
		Telephony:  "CSI/O model with line and DTMF inputs",
		Eeprom:     eeprom,
		Coin:       coin,
		Card:       fmt.Sprintf("Track 2 sensors/DMA%d; GPM103 image read/debit/carry", m.Board.CardDMA),
		Management: "ASCI1 raw TCP bridge (NCC or TTY)",
		Audio:      "voice events only",
	}
	if st.Breakpoints == nil {
		st.Breakpoints = []Breakpoint{}
	}
	if st.Watchpoints == nil {
		st.Watchpoints = []Watchpoint{}
	}
	if st.Devices.CoinEscrow == nil {
		st.Devices.CoinEscrow = []int{}
	}
	if st.Devices.UnknownCommands == nil {
		st.Devices.UnknownCommands = ByteList{}
	}
	return st
}
