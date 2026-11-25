package server

import (
	"bytes"
	"encoding/json"
	"strings"

	"millennium/emu"
)

var regWidths = map[string]int64{
	"a": 255, "f": 255, "b": 255, "c": 255, "d": 255, "e": 255, "h": 255, "l": 255,
	"a2": 255, "f2": 255, "b2": 255, "c2": 255, "d2": 255, "e2": 255, "h2": 255, "l2": 255,
	"i": 255, "r": 255, "ix": 65535, "iy": 65535, "sp": 65535, "pc": 65535, "im": 2,
}

// orderedBodyKeys returns top-level body keys in JSON document order.
func orderedBodyKeys(raw []byte) []string {
	keys := []string{}
	if len(raw) == 0 {
		return keys
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	depth := 0
	expectKey := false
	for {
		t, err := dec.Token()
		if err != nil {
			break
		}
		if d, ok := t.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
				if depth == 1 {
					expectKey = true
				}
			case '}', ']':
				depth--
				if depth == 0 {
					return keys
				}
				if depth == 1 {
					expectKey = true
				}
			}
			continue
		}
		if depth == 1 {
			if expectKey {
				if s, ok := t.(string); ok {
					keys = append(keys, s)
				}
			}
			expectKey = !expectKey
		}
	}
	return keys
}

func (a *App) setRegisters(b map[string]any, rawBody []byte) (emu.CPUState, error) {
	keys := orderedBodyKeys(rawBody)
	if len(keys) == 0 {
		for k := range b {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		width, ok := regWidths[k]
		if !ok {
			return emu.CPUState{}, emu.Err("Register " + k + " is not writable")
		}
		v, err := emu.Number(b[k], k, 0, width)
		if err != nil {
			return emu.CPUState{}, err
		}
		c := &a.Machine.CPU
		n := int(v)
		switch k {
		case "a":
			c.A = uint8(n)
		case "f":
			c.F = uint8(n)
		case "b":
			c.B = uint8(n)
		case "c":
			c.C = uint8(n)
		case "d":
			c.D = uint8(n)
		case "e":
			c.E = uint8(n)
		case "h":
			c.H = uint8(n)
		case "l":
			c.L = uint8(n)
		case "a2":
			c.A2 = uint8(n)
		case "f2":
			c.F2 = uint8(n)
		case "b2":
			c.B2 = uint8(n)
		case "c2":
			c.C2 = uint8(n)
		case "d2":
			c.D2 = uint8(n)
		case "e2":
			c.E2 = uint8(n)
		case "h2":
			c.H2 = uint8(n)
		case "l2":
			c.L2 = uint8(n)
		case "i":
			c.I = uint8(n)
		case "r":
			c.R = uint8(n)
		case "ix":
			c.IX = uint16(n)
		case "iy":
			c.IY = uint16(n)
		case "sp":
			c.SP = uint16(n)
		case "pc":
			c.PC = uint16(n)
		case "im":
			c.IM = n
		}
	}
	return a.Machine.GetCPUState(), nil
}

// handlePost2 serves mutation routes (second half). Call with Mu held.
func (a *App) handlePost2(route string, b map[string]any, bRaw map[string]json.RawMessage, rawBody []byte) (*apiResponse, error) {
	m := a.Machine
	switch route {
	case "/api/registers":
		if err := a.paused(); err != nil {
			return nil, err
		}
		st, err := a.setRegisters(b, rawBody)
		if err != nil {
			return nil, err
		}
		r, err2 := okResp(st)
		return r, err2
	case "/api/breakpoints":
		if err := a.paused(); err != nil {
			return nil, err
		}
		if b["clear"] == true {
			m.Breakpoints = []emu.Breakpoint{}
		} else {
			s, err := spaceOf(b["space"], "physical")
			if err != nil {
				return nil, err
			}
			maxAddr := int64(65535)
			if s == "physical" {
				maxAddr = 0xfffff
			}
			address, err := emu.Number(b["address"], "address", 0, maxAddr)
			if err != nil {
				return nil, err
			}
			bp := emu.Breakpoint{Space: s, Address: int(address)}
			if v, ok := b["bbr"]; ok {
				n, err := emu.Number(v, "bbr", 0, 255)
				if err != nil {
					return nil, err
				}
				ni := int(n)
				bp.Bbr = &ni
			}
			if len(m.Breakpoints) >= 256 {
				return nil, emu.Err("Too many breakpoints")
			}
			m.Breakpoints = append(m.Breakpoints, bp)
		}
		r, err := okResp(m.Breakpoints)
		return r, err
	case "/api/watchpoints":
		if err := a.paused(); err != nil {
			return nil, err
		}
		if b["clear"] == true {
			m.Watchpoints = []emu.Watchpoint{}
		} else {
			kind, _ := b["kind"].(string)
			if kind != "read" && kind != "write" && kind != "in" && kind != "out" {
				return nil, emu.Err("kind must be read/write/in/out")
			}
			s, err := spaceOf(b["space"], "physical")
			if err != nil {
				return nil, err
			}
			if len(m.Watchpoints) >= 256 {
				return nil, emu.Err("Too many watchpoints")
			}
			maxAddr := int64(65535)
			if s == "physical" {
				maxAddr = 0xfffff
			}
			address, err := emu.Number(b["address"], "address", 0, maxAddr)
			if err != nil {
				return nil, err
			}
			length, err := emu.Number(orDefault(b["length"], 1), "length", 1, 65536)
			if err != nil {
				return nil, err
			}
			m.Watchpoints = append(m.Watchpoints, emu.Watchpoint{Kind: kind, Space: s,
				Address: int(address), Length: int(length)})
		}
		r, err := okResp(m.Watchpoints)
		return r, err
	case "/api/trace":
		m.TraceEnabled = b["enabled"] == true
		limit, err := emu.Number(orDefault(b["limit"], 1000), "limit", 1, 10000)
		if err != nil {
			return nil, err
		}
		m.TraceLimit = int(limit)
		if b["clear"] == true {
			m.Trace = []emu.TraceRow{}
		}
		r, err2 := okResp(struct {
			Enabled bool `json:"enabled"`
			Limit   int  `json:"limit"`
		}{m.TraceEnabled, m.TraceLimit})
		return r, err2
	case "/api/devices":
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		typ := jsString(b["type"])
		dataRaw := rawOf(bRaw, b, "data")
		if dataRaw == nil || string(dataRaw) == "null" {
			dataRaw = json.RawMessage("{}")
		}
		if err := m.DeviceEvent(typ, emu.NewEventDataRaw(dataRaw), true); err != nil {
			return nil, err
		}
		r, err := okResp(a.Result())
		return r, err
	case "/api/replay":
		return a.handleReplay(b, bRaw)
	case "/api/snapshots":
		return a.handleSnapshotsPost(b)
	case "/api/snapshots/restore":
		return a.handleSnapshotRestore(b)
	case "/api/snapshot":
		if err := a.paused(); err != nil {
			return nil, err
		}
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		a.Ncc.EndSession("snapshot restored")
		raw := rawBody
		if len(raw) == 0 {
			raw = jsonOf(b)
		}
		if err := m.Restore(raw); err != nil {
			return nil, err
		}
		a.Ncc.SetLastCycle(m.CPU.Cycles)
		r, err := okResp(a.Result())
		return r, err
	case "/api/patches":
		if err := a.paused(); err != nil {
			return nil, err
		}
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		entry, err := m.Patch(b["offset"], b["expected"], b["hex"], b["inputSha256"])
		if err != nil {
			return nil, err
		}
		r, err2 := okResp(entry)
		return r, err2
	case "/api/patches/revert":
		if err := a.paused(); err != nil {
			return nil, err
		}
		last, err := m.RevertPatch()
		if err != nil {
			return nil, err
		}
		r, err2 := okResp(last)
		return r, err2
	case "/api/scenario":
		return a.handleScenario(b)
	}
	return nil, nil
}

func (a *App) handleReplay(b map[string]any, bRaw map[string]json.RawMessage) (*apiResponse, error) {
	m := a.Machine
	if err := a.paused(); err != nil {
		return nil, err
	}
	if err := a.requireLoaded(); err != nil {
		return nil, err
	}
	events, isArr := b["events"].([]any)
	var eventsRaw []json.RawMessage
	if r := rawOf(bRaw, b, "events"); r != nil {
		_ = json.Unmarshal(r, &eventsRaw)
	}
	if !isArr || len(events) > 10000 || len(m.Devices.Scheduled)+len(events) > 10000 {
		return nil, emu.Err("Scheduled events limit is 10000")
	}
	validated := make([]emu.ScheduledEvent, 0, len(events))
	for i, ev := range events {
		em, _ := ev.(map[string]any)
		if em == nil {
			em = map[string]any{}
		}
		var dataRaw json.RawMessage
		if i < len(eventsRaw) {
			var sub map[string]json.RawMessage
			if json.Unmarshal(eventsRaw[i], &sub) == nil {
				dataRaw = sub["data"]
			}
		}
		dm, _ := em["data"].(map[string]any)
		if dataRaw == nil || string(dataRaw) == "null" {
			if dm == nil {
				dataRaw = json.RawMessage("{}")
			} else {
				dataRaw = jsonOf(dm)
			}
		}
		if dm == nil {
			dm = map[string]any{}
		}
		if err := emu.ValidateInput(jsString(em["type"]), dm); err != nil {
			return nil, err
		}
		cycle, err := emu.Number(em["cycle"], "cycle", m.CPU.Cycles, emu.MaxSafeInteger)
		if err != nil {
			return nil, err
		}
		validated = append(validated, emu.ScheduledEvent{Cycle: float64(cycle),
			Type: jsString(em["type"]), Data: emu.NewEventDataRaw(dataRaw)})
	}
	m.Devices.Scheduled = append(m.Devices.Scheduled, validated...)
	r, err := okResp(struct {
		Scheduled int `json:"scheduled"`
	}{len(validated)})
	return r, err
}

func (a *App) handleSnapshotsPost(b map[string]any) (*apiResponse, error) {
	if err := a.paused(); err != nil {
		return nil, err
	}
	if err := a.requireLoaded(); err != nil {
		return nil, err
	}
	name := "default"
	if v, ok := b["name"]; ok && v != nil {
		name = jsString(v)
	}
	if _, exists := a.snapshots[name]; !exists && len(a.snapshots) >= 16 {
		return nil, emu.Err("Maximum 16 snapshots")
	}
	raw, err := a.Machine.Snapshot()
	if err != nil {
		return nil, err
	}
	if _, exists := a.snapshots[name]; !exists {
		a.snapshotOrder = append(a.snapshotOrder, name)
	}
	a.snapshots[name] = raw
	r, err := okResp(struct {
		Name    string `json:"name"`
		RomHash string `json:"romHash"`
	}{name, emu.Hash(a.Machine.ROM)})
	return r, err
}

func (a *App) handleSnapshotRestore(b map[string]any) (*apiResponse, error) {
	if err := a.paused(); err != nil {
		return nil, err
	}
	name := "default"
	if v, ok := b["name"]; ok && v != nil {
		name = jsString(v)
	}
	saved, ok := a.snapshots[name]
	if !ok {
		return nil, emu.Err("Snapshot not found")
	}
	a.Ncc.EndSession("snapshot restored")
	if err := a.Machine.Restore(saved); err != nil {
		return nil, err
	}
	a.Ncc.SetLastCycle(a.Machine.CPU.Cycles)
	r, err := okResp(a.Result())
	return r, err
}

func (a *App) handleScenario(b map[string]any) (*apiResponse, error) {
	m := a.Machine
	if err := a.paused(); err != nil {
		return nil, err
	}
	if err := a.requireLoaded(); err != nil {
		return nil, err
	}
	steps, isArr := b["steps"].([]any)
	if !isArr || len(steps) > 100 {
		return nil, emu.Err("steps must be an array (max 100)")
	}
	results := []any{}
	budget := int64(5000000)
	for _, step := range steps {
		sm, ok := step.(map[string]any)
		if !ok {
			continue
		}
		if ev, truthy := sm["event"]; truthy && ev != nil && ev != false {
			em, _ := ev.(map[string]any)
			if em == nil {
				em = map[string]any{}
			}
			dataRaw := json.RawMessage("{}")
			if dm, ok := em["data"].(map[string]any); ok && dm != nil {
				dataRaw = jsonOf(dm)
			}
			if err := m.DeviceEvent(jsString(em["type"]), emu.NewEventDataRaw(dataRaw), true); err != nil {
				return nil, err
			}
		}
		if rv, truthy := sm["run"]; truthy && rv != nil && rv != false {
			rm, _ := rv.(map[string]any)
			if rm == nil {
				rm = map[string]any{}
			}
			n, err := emu.Number(orDefault(rm["instructions"], 100000), "instructions", 1, budget)
			if err != nil {
				return nil, err
			}
			budget -= n
			exec, err := m.Run(emu.RunOptions{Instructions: n, Cycles: int64(^uint64(0) >> 1)})
			if err != nil {
				return nil, err
			}
			results = append(results, struct {
				Execution emu.RunResult `json:"execution"`
			}{exec})
			if exec.Reason != "budget" {
				break
			}
		}
		if expected, exists := sm["expectDisplay"]; exists {
			lines := a.Result().Display
			want := jsString(expected)
			if expected == nil {
				want = "null"
			}
			passed := strings.Contains(lines[0], want) || strings.Contains(lines[1], want)
			results = append(results, struct {
				Assertion string    `json:"assertion"`
				Expected  any       `json:"expected"`
				Actual    [2]string `json:"actual"`
				Passed    bool      `json:"passed"`
			}{"display includes", expected, lines, passed})
			if !passed {
				break
			}
		}
	}
	r, err := okResp(struct {
		Results []any      `json:"results"`
		State   ResultData `json:"state"`
	}{results, a.Result()})
	return r, err
}
