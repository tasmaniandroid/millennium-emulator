package server

import (
	"encoding/hex"
	"strings"

	"millennium/disasm"
	"millennium/emu"
)

type disasmRow struct {
	Address  int      `json:"address"`
	Bytes    string   `json:"bytes"`
	Text     string   `json:"text"`
	Physical int      `json:"physical"`
	Symbols  []string `json:"symbols"`
}

func (a *App) disasmRows(address, count int) []disasmRow {
	m := a.Machine
	rows := disasm.Disassemble(func(addr int) int { return m.ReadPhysical(m.Physical(addr)) }, address, count)
	out := make([]disasmRow, 0, len(rows))
	for _, r := range rows {
		syms := []string{}
		phys := m.Physical(r.Address)
		for _, s := range m.Symbols {
			if s.Physical == phys {
				syms = append(syms, s.Name)
			}
		}
		out = append(out, disasmRow{Address: r.Address, Bytes: r.Bytes, Text: r.Text,
			Physical: phys, Symbols: syms})
	}
	return out
}

// handleGet serves all GET /api/* routes. Call with Mu held.
func (a *App) handleGet(route string, q map[string]string) (*apiResponse, error) {
	m := a.Machine
	switch route {
	case "/api/boards":
		return okResp(emu.BoardValues())
	case "/api/firmwares":
		return okResp(a.Firmwares())
	case "/api/attach":
		return okResp(a.Sources())
	case "/api/state":
		return okResp(a.Result())
	case "/api/speed":
		return okResp(a.realtime)
	case "/api/ncc":
		return okResp(a.Ncc.State())
	case "/api/cards":
		return okResp(m.Devices.Reader)
	case "/api/memory":
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		s := "logical"
		if q["space"] == "flash" {
			s = "flash"
		} else {
			var err error
			if v, ok := q["space"]; ok {
				s, err = spaceOf(v, "logical")
			} else {
				s, err = spaceOf(nil, "logical")
			}
			if err != nil {
				return nil, err
			}
		}
		maxAddr := int64(0xfffff)
		if s == "logical" {
			maxAddr = 65535
		}
		address, err := qNum(q, "address", 0, "address", 0, maxAddr)
		if err != nil {
			return nil, err
		}
		length, err := qNum(q, "length", 256, "length", 1, 65536)
		if err != nil {
			return nil, err
		}
		limit := int64(0x100000)
		if s == "logical" {
			limit = 65536
		}
		if address+length > limit {
			return nil, emu.Err("Memory range exceeds address space")
		}
		src := m.Flash
		if src == nil {
			src = m.ROM
		}
		data := make([]byte, length)
		for i := int64(0); i < length; i++ {
			addr := address + i
			switch s {
			case "flash":
				if addr < int64(len(src)) {
					data[i] = src[addr]
				} else {
					data[i] = 255
				}
			case "logical":
				data[i] = byte(m.ReadPhysical(m.Physical(int(addr))))
			default:
				data[i] = byte(m.ReadPhysical(int(addr)))
			}
		}
		return okResp(struct {
			Space   string `json:"space"`
			Address int64  `json:"address"`
			Length  int64  `json:"length"`
			Hex     string `json:"hex"`
		}{s, address, length, hex.EncodeToString(data)})
	case "/api/disassembly":
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		var address int64
		var err error
		if v, ok := q["address"]; ok {
			address, err = emu.Number(v, "address", 0, 65535)
		} else {
			address, err = emu.Number(float64(m.CPU.PC), "address", 0, 65535)
		}
		if err != nil {
			return nil, err
		}
		count, err := qNum(q, "count", 24, "count", 1, 256)
		if err != nil {
			return nil, err
		}
		return okResp(a.disasmRows(int(address), int(count)))
	case "/api/symbols":
		out := []emu.Symbol{}
		search := strings.ToLower(q["search"])
		for _, s := range m.Symbols {
			if q["search"] == "" || strings.Contains(strings.ToLower(s.Name), search) {
				out = append(out, s)
			}
		}
		return okResp(out)
	case "/api/ports":
		return okResp(struct {
			Hex string `json:"hex"`
		}{hex.EncodeToString(m.Ports)})
	case "/api/trace":
		rows := m.Trace
		if rows == nil {
			rows = []emu.TraceRow{}
		}
		return okResp(rows)
	case "/api/events":
		since, err := qNum(q, "since", 0, "since", 0, emu.MaxSafeInteger)
		if err != nil {
			return nil, err
		}
		out := []emu.Event{}
		for _, e := range m.Events {
			if e.ID > since {
				out = append(out, e)
			}
		}
		return okResp(out)
	case "/api/inputs":
		log := m.InputLog
		if log == nil {
			log = []emu.InputLogEntry{}
		}
		return okResp(log)
	case "/api/snapshots":
		return okResp(a.snapshotOrder)
	case "/api/snapshot":
		if err := a.paused(); err != nil {
			return nil, err
		}
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		raw, err := m.Snapshot()
		if err != nil {
			return nil, err
		}
		return &apiResponse{Status: 200, JSON: raw}, nil
	case "/api/patches":
		p := m.Patches
		if p == nil {
			p = []emu.Patch{}
		}
		return okResp(p)
	case "/api/firmware/export":
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		return &apiResponse{Status: 200, Binary: m.ROM, BinaryName: "millennium-patched.bin"}, nil
	case "/api/flash/export":
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		src := m.Flash
		if src == nil {
			src = m.ROM
		}
		out := append([]byte(nil), src...)
		return &apiResponse{Status: 200, Binary: out, BinaryName: "millennium-flash.bin"}, nil
	}
	return nil, nil
}
