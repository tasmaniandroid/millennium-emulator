package server

import (
	"math"
	"time"

	"millennium/emu"
	"millennium/ws"
)

type displayFrame struct {
	emu.DisplayStateData
	View ViewData `json:"view"`
}

// StartTick runs the 10 ms realtime scheduler until stop is closed.
func (a *App) StartTick(stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			a.tickOnce()
		}
	}
}

func (a *App) tickOnce() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	now := nowMs()
	elapsed := math.Min(now-a.lastTick, 250)
	a.lastTick = now
	a.Ncc.Pump()
	m := a.Machine
	if m.Running {
		freshRun := !a.wasRunning || m.CPU.Cycles < a.sampleCycles
		if freshRun {
			a.cycleDebt = 0
			a.sampleWall = now
			a.sampleCycles = m.CPU.Cycles
		}
		cyclesPerMs := float64(m.Board.ClockHz) / 1000
		add := elapsed * cyclesPerMs * a.realtime.Factor
		if freshRun {
			add = 0
		}
		a.cycleDebt = math.Min(a.cycleDebt+add, cyclesPerMs*a.realtime.Factor*250)
		// Eight milliseconds of host work per turn keeps HTTP and TCP I/O
		// responsive. Short batches let the modem/transport follow fast runs.
		deadline := nowMs() + 8
		for m.Running && a.cycleDebt > 0 && nowMs() < deadline {
			before := m.CPU.Cycles
			cyc := int64(math.Ceil(math.Min(a.cycleDebt, 491520)))
			if cyc < 1 {
				cyc = 1
			}
			_, _ = m.Run(emu.RunOptions{Instructions: 2048, Cycles: cyc})
			spent := m.CPU.Cycles - before
			a.cycleDebt -= float64(spent)
			a.Ncc.Pump()
			if spent == 0 {
				break
			}
		}
		wall := nowMs() - a.sampleWall
		if wall >= 1000 {
			a.realtime.AchievedFactor = float64(m.CPU.Cycles-a.sampleCycles) / (wall * cyclesPerMs)
			a.sampleWall = nowMs()
			a.sampleCycles = m.CPU.Cycles
		}
	} else {
		a.cycleDebt = 0
		a.realtime.AchievedFactor = 0
		a.sampleWall = now
		a.sampleCycles = m.CPU.Cycles
	}
	a.wasRunning = m.Running
	a.broadcast()
}

func (a *App) broadcast() {
	m := a.Machine
	visible := make([]*ws.Peer, 0, len(a.peers))
	for p, st := range a.peers {
		if st.subVisible {
			visible = append(visible, p)
		}
	}
	displayClients := []*sseClient{}
	for c := range a.clients {
		if c.display {
			displayClients = append(displayClients, c)
		}
	}
	if len(a.clients) == 0 && len(visible) == 0 {
		return
	}
	clock := nowMs()
	d := m.Devices
	if (len(displayClients) > 0 || len(visible) > 0) && clock-a.lastDisplayBroadcast >= 25 {
		changed := d.DisplayRevision != a.lastDisplayRevision || d != a.lastDisplayBuffer
		if changed {
			frame := displayFrame{DisplayStateData: m.DisplayState(), View: a.View()}
			frameJSON := jsonOf(frame)
			for _, c := range displayClients {
				c.write("display", frameJSON)
			}
			env := jsonOf(envelope{Type: "display", Data: frame})
			for _, p := range visible {
				p.Send(env, true)
			}
			a.lastDisplayBroadcast = clock
			a.lastDisplayRevision = d.DisplayRevision
			a.lastDisplayBuffer = d
		}
	}
	if time.Since(a.lastBroadcast) >= 200*time.Millisecond {
		state := a.Result()
		stateJSON := jsonOf(state)
		for c := range a.clients {
			c.write("state", stateJSON)
		}
		frame := SocketStateData{ResultData: state, NccState: a.Ncc.State(),
			PatchManifest: m.Patches}
		env := jsonOf(envelope{Type: "state", Data: frame})
		for _, p := range visible {
			st := a.peers[p]
			p.Send(env, true)
			reset := st.eventDevices != m.Devices || st.lastEventID > m.EventID
			rows := []emu.Event{}
			for _, e := range m.Events {
				if reset || e.ID > st.lastEventID {
					rows = append(rows, e)
				}
			}
			if len(rows) > 150 {
				rows = rows[len(rows)-150:]
			}
			if reset || len(rows) > 0 {
				eventsEnv := jsonOf(envelope{Type: "events",
					Data: struct {
						Reset bool        `json:"reset"`
						Rows  []emu.Event `json:"rows"`
					}{reset, rows}})
				if p.Send(eventsEnv, true) {
					st.lastEventID = m.EventID
					st.eventDevices = m.Devices
				}
			}
		}
		a.lastBroadcast = time.Now()
	}
	for _, p := range visible {
		st := a.peers[p]
		if st.subDebugger && clock-st.lastDebugger >= 1000 {
			address := int(m.CPU.PC)
			if st.subAddress != nil {
				address = *st.subAddress
			}
			rows := []disasmRow{}
			if len(m.ROM) > 0 {
				rows = a.disasmRows(address, 24)
			}
			env := jsonOf(envelope{Type: "debugger", Data: struct {
				Rows []disasmRow `json:"rows"`
			}{rows}})
			if p.Send(env, true) {
				st.lastDebugger = clock
			}
		}
	}
}
