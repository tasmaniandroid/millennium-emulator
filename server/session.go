package server

import (
	"encoding/json"
	"os"

	"millennium/emu"
)

type sessionPatch struct {
	Offset      any    `json:"offset"`
	Before      string `json:"before"`
	After       string `json:"after"`
	InputSha256 string `json:"inputSha256"`
}

type sessionCounters struct {
	Received    any `json:"received"`
	Transmitted any `json:"transmitted"`
}

type sessionFile struct {
	FirmwarePath     string           `json:"firmwarePath"`
	Board            *string          `json:"board"`
	Patches          []sessionPatch   `json:"patches"`
	Snapshot         json.RawMessage  `json:"snapshot"`
	Breakpoints      []emu.Breakpoint `json:"breakpoints"`
	Watchpoints      []emu.Watchpoint `json:"watchpoints"`
	RealtimeFactor   *float64         `json:"realtimeFactor"`
	Ncc              map[string]any   `json:"ncc"`
	TransportCounter *sessionCounters `json:"transportCounters"`
}

// RestoreSessionFile reloads a saved .session.json:
// load firmware, re-apply patches, restore the snapshot, then debugger
// settings, speed, NCC target and transport counters. The session is
// restored paused unless the caller resumes it.
func (a *App) RestoreSessionFile(path string) (firmwarePath string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var session sessionFile
	if err := json.Unmarshal(raw, &session); err != nil {
		return "", err
	}

	a.Mu.Lock()
	defer a.Mu.Unlock()

	board := "auto"
	if session.Board != nil && *session.Board != "" {
		board = *session.Board
	} else if len(session.Snapshot) > 0 {
		var snapHead struct {
			Board string `json:"board"`
		}
		if json.Unmarshal(session.Snapshot, &snapHead) == nil && snapHead.Board != "" {
			board = snapHead.Board
		}
	}
	if _, err := a.Load(session.FirmwarePath, nil, board); err != nil {
		return "", err
	}
	for _, p := range session.Patches {
		if _, err := a.Machine.Patch(p.Offset, p.Before, p.After, p.InputSha256); err != nil {
			return "", err
		}
	}
	if err := a.Machine.Restore(session.Snapshot); err != nil {
		return "", err
	}
	if session.Breakpoints != nil {
		a.Machine.Breakpoints = session.Breakpoints
	} else {
		a.Machine.Breakpoints = []emu.Breakpoint{}
	}
	if session.Watchpoints != nil {
		a.Machine.Watchpoints = session.Watchpoints
	} else {
		a.Machine.Watchpoints = []emu.Watchpoint{}
	}
	if session.RealtimeFactor != nil {
		if _, err := a.SetSpeed(*session.RealtimeFactor); err != nil {
			return "", err
		}
	}
	if session.Ncc != nil {
		if _, err := a.Ncc.Configure(session.Ncc); err != nil {
			return "", err
		}
	}
	if session.TransportCounter != nil {
		received, err := emu.Number(session.TransportCounter.Received, "received counter", 0, emu.MaxSafeInteger)
		if err != nil {
			return "", err
		}
		transmitted, err := emu.Number(session.TransportCounter.Transmitted, "transmitted counter", 0, emu.MaxSafeInteger)
		if err != nil {
			return "", err
		}
		a.Ncc.Received = received
		a.Ncc.Transmitted = transmitted
	}
	return session.FirmwarePath, nil
}
