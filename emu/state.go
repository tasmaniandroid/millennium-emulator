package emu

import (
	"encoding/base64"
	"encoding/json"
)

// CPUState is the CPU register payload; field order is wire format.
type CPUState struct {
	A            int   `json:"a"`
	F            int   `json:"f"`
	B            int   `json:"b"`
	C            int   `json:"c"`
	D            int   `json:"d"`
	E            int   `json:"e"`
	H            int   `json:"h"`
	L            int   `json:"l"`
	A2           int   `json:"a2"`
	F2           int   `json:"f2"`
	B2           int   `json:"b2"`
	C2           int   `json:"c2"`
	D2           int   `json:"d2"`
	E2           int   `json:"e2"`
	H2           int   `json:"h2"`
	L2           int   `json:"l2"`
	IX           int   `json:"ix"`
	IY           int   `json:"iy"`
	SP           int   `json:"sp"`
	PC           int   `json:"pc"`
	I            int   `json:"i"`
	R            int   `json:"r"`
	IFF1         bool  `json:"iff1"`
	IFF2         bool  `json:"iff2"`
	IM           int   `json:"im"`
	Halted       bool  `json:"halted"`
	EIDelay      int   `json:"eiDelay"`
	Cycles       int64 `json:"cycles"`
	Instructions int64 `json:"instructions"`
}

func (m *Machine) GetCPUState() CPUState {
	c := &m.CPU
	return CPUState{A: int(c.A), F: int(c.F), B: int(c.B), C: int(c.C),
		D: int(c.D), E: int(c.E), H: int(c.H), L: int(c.L), A2: int(c.A2), F2: int(c.F2),
		B2: int(c.B2), C2: int(c.C2), D2: int(c.D2), E2: int(c.E2), H2: int(c.H2),
		L2: int(c.L2), IX: int(c.IX), IY: int(c.IY), SP: int(c.SP), PC: int(c.PC),
		I: int(c.I), R: int(c.R), IFF1: c.IFF1, IFF2: c.IFF2, IM: c.IM,
		Halted: c.Halted, EIDelay: c.EIDelay, Cycles: c.Cycles, Instructions: c.Instructions}
}

// ApplyCPUState writes validated register values back into the core.
func (m *Machine) ApplyCPUState(s CPUState) {
	c := &m.CPU
	c.A, c.F, c.B, c.C = uint8(s.A), uint8(s.F), uint8(s.B), uint8(s.C)
	c.D, c.E, c.H, c.L = uint8(s.D), uint8(s.E), uint8(s.H), uint8(s.L)
	c.A2, c.F2 = uint8(s.A2), uint8(s.F2)
	c.B2, c.C2 = uint8(s.B2), uint8(s.C2)
	c.D2, c.E2 = uint8(s.D2), uint8(s.E2)
	c.H2, c.L2 = uint8(s.H2), uint8(s.L2)
	c.IX, c.IY = uint16(s.IX), uint16(s.IY)
	c.SP, c.PC = uint16(s.SP), uint16(s.PC)
	c.I, c.R = uint8(s.I), uint8(s.R)
	c.IFF1, c.IFF2 = s.IFF1, s.IFF2
	c.IM, c.Halted, c.EIDelay = s.IM, s.Halted, s.EIDelay
	c.Cycles, c.Instructions = s.Cycles, s.Instructions
}

// SnapshotData is the snapshot payload with exact key order. Version 1 is
// MTR1 (memory only); version 2 is MTR2 and adds the separate flash region.
// memory/ports/flash are standard-base64 of the raw byte regions; devices
// and inputLog serialize with
// stable field order and raw event payloads, so snapshot files remain
// interchangeable across versions (guarded by the differential harness).
type SnapshotData struct {
	Version  int             `json:"version"`
	Board    string          `json:"board"`
	Flash    *string         `json:"flash,omitempty"`
	RomHash  string          `json:"romHash"`
	Cpu      CPUState        `json:"cpu"`
	Memory   string          `json:"memory"`
	Ports    string          `json:"ports"`
	Devices  *Devices        `json:"devices"`
	InputLog []InputLogEntry `json:"inputLog"`
}

// Snapshot returns the full machine snapshot as JSON.
func (m *Machine) Snapshot() (json.RawMessage, error) {
	s := SnapshotData{
		Version:  1,
		Board:    m.Board.ID,
		RomHash:  Hash(m.ROM),
		Cpu:      m.GetCPUState(),
		Memory:   base64.StdEncoding.EncodeToString(m.Memory),
		Ports:    base64.StdEncoding.EncodeToString(m.Ports),
		InputLog: m.InputLog,
	}
	if m.Board.Family == "mtr2" {
		s.Version = 2
		f := base64.StdEncoding.EncodeToString(m.Flash)
		s.Flash = &f
	}
	// Deep-clone devices like structuredClone: round-trip through JSON.
	devRaw, err := json.Marshal(m.Devices)
	if err != nil {
		return nil, err
	}
	devices := &Devices{}
	if err := json.Unmarshal(devRaw, devices); err != nil {
		return nil, err
	}
	s.Devices = devices
	if s.InputLog == nil {
		s.InputLog = []InputLogEntry{}
	}
	return json.Marshal(s)
}
