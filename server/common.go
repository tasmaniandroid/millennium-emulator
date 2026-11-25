// Package server hosts the emulator: HTTP/WS/SSE control plane, embedded
// browser frontend and the realtime execution scheduler.
package server

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"

	"millennium/emu"
	"millennium/ncc"
	"millennium/ws"
)

// RealtimeData is the speed payload: target factor, achieved factor and
// board clock.
type RealtimeData struct {
	Factor         float64 `json:"factor"`
	AchievedFactor float64 `json:"achievedFactor"`
	ClockHz        int     `json:"clockHz"`
}

// ViewData is the freshness token clients use to drop stale frames.
type ViewData struct {
	Session  string `json:"session"`
	Sequence int64  `json:"sequence"`
}

// ResultData is the standard response envelope: full machine state,
// realtime settings and a fresh view token.
type ResultData struct {
	emu.MachineState
	Realtime RealtimeData `json:"realtime"`
	View     ViewData     `json:"view"`
}

// SocketStateData is the WebSocket state push: the result envelope plus
// NCC status and the patch manifest.
type SocketStateData struct {
	ResultData
	NccState      ncc.StateData `json:"nccState"`
	PatchManifest []emu.Patch   `json:"patchManifest"`
}

type sseClient struct {
	write   func(event string, data []byte)
	closed  chan struct{}
	once    sync.Once
	display bool
}

type peerState struct {
	subVisible   bool
	subDebugger  bool
	subAddress   *int
	lastEventID  int64
	eventDevices *emu.Devices
	lastDebugger float64
}

// App is the emulator server application state.
type App struct {
	Machine *emu.Machine
	Ncc     *ncc.Bridge
	Mu      sync.Mutex

	sources       []*Source
	nextSourceID  int
	snapshots     map[string]json.RawMessage
	snapshotOrder []string
	clients       map[*sseClient]bool
	peers         map[*ws.Peer]*peerState

	realtime     RealtimeData
	viewSession  string
	viewSequence int64

	cycleDebt    float64
	lastTick     float64
	sampleWall   float64
	sampleCycles int64
	wasRunning   bool

	lastBroadcast        time.Time
	lastDisplayBroadcast float64
	lastDisplayRevision  int64
	lastDisplayBuffer    *emu.Devices
}

func nowMs() float64 { return float64(time.Now().UnixNano()) / 1e6 }

// CreateApp builds an app with no firmware attached; sources are attached
// at runtime via POST /api/attach or the UI.
func CreateApp() *App {
	a := &App{
		Machine:       emu.NewMachine("mtr1"),
		snapshotOrder: []string{},
		nextSourceID:  1,
		clients:       map[*sseClient]bool{},
		peers:         map[*ws.Peer]*peerState{},
		realtime:      RealtimeData{Factor: 4, AchievedFactor: 0, ClockHz: 6144000},
	}
	a.snapshots = map[string]json.RawMessage{}
	a.viewSession = uuid.NewString()
	a.Ncc = ncc.NewBridge(&a.Mu, a.Machine)
	a.cycleDebt = 0
	a.lastTick = nowMs()
	a.sampleWall = a.lastTick
	a.sampleCycles = 0
	a.lastBroadcast = time.Now()
	return a
}

// View returns the next freshness token (sequence increments per call).
// Call with Mu held.
func (a *App) View() ViewData {
	a.viewSequence++
	return ViewData{Session: a.viewSession, Sequence: a.viewSequence}
}

// Result builds the standard response envelope. Call with Mu held.
func (a *App) Result() ResultData {
	return ResultData{MachineState: a.Machine.State(), Realtime: a.realtime, View: a.View()}
}

// SocketState builds the WebSocket state push. Call with Mu held.
func (a *App) SocketState() SocketStateData {
	return SocketStateData{ResultData: a.Result(), NccState: a.Ncc.State(),
		PatchManifest: a.Machine.Patches}
}

// SetSpeed applies a new realtime target factor (0.1..64) and re-bases the
// scheduler's cycle-debt accounting. Call with Mu held.
func (a *App) SetSpeed(factor any) (RealtimeData, error) {
	f, ok := factor.(float64)
	if !ok || f != f || f < 0.1 || f > 64 {
		return a.realtime, emu.Err("factor must be a number from 0.1 to 64")
	}
	a.realtime.Factor = f
	a.realtime.AchievedFactor = 0
	a.cycleDebt = 0
	a.lastTick = nowMs()
	a.sampleWall = a.lastTick
	a.sampleCycles = a.Machine.CPU.Cycles
	return a.realtime, nil
}

func (a *App) paused() error {
	if a.Machine.Running {
		return emu.Err("Pause execution before modifying the machine")
	}
	return nil
}

func (a *App) requireLoaded() error {
	if len(a.Machine.ROM) == 0 {
		return emu.Err("Load firmware first")
	}
	return nil
}
