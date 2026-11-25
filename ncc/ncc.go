// Package ncc implements the transparent, call-scoped TCP transport between
// the firmware's ASCI1 serial channel and an external NCC/TTY target.
package ncc

import (
	"net"
	"sync"

	"millennium/emu"
)

const Protocol = "call-scoped raw TCP ↔ ASCI1; opens on first firmware TX; no packet validation"

type StateData struct {
	Mode                 string  `json:"mode"`
	Host                 string  `json:"host"`
	Port                 int     `json:"port"`
	Armed                bool    `json:"armed"`
	Connecting           bool    `json:"connecting"`
	Connected            bool    `json:"connected"`
	AutoHandshake        bool    `json:"autoHandshake"`
	Handshake            string  `json:"handshake"`
	Received             int64   `json:"received"`
	Transmitted          int64   `json:"transmitted"`
	PendingBytes         int     `json:"pendingBytes"`
	PendingTransmitBytes int     `json:"pendingTransmitBytes"`
	DrainingRemoteEOF    bool    `json:"drainingRemoteEOF"`
	ReceiveDelayMs       int     `json:"receiveDelayMs"`
	Error                *string `json:"error"`
	Protocol             string  `json:"protocol"`
}

// Bridge is the transport. Mu is the server-wide machine lock: exported
// methods must be called with Mu held (socket goroutines lock it themselves).
type Bridge struct {
	Mu *sync.Mutex
	M  *emu.Machine

	Mode string
	Host string
	Port int

	errStr           *string
	Received         int64
	Transmitted      int64
	rx               []byte
	rxReadyAt        []float64
	tx               []byte
	ReceiveDelayMs   int
	AutoHandshake    bool
	handshake        string
	phaseCycle       float64
	generation       int
	conn             net.Conn
	connState        int // 0 none, 1 connecting, 2 connected
	writePending     int
	writeQ           [][]byte
	writeCond        *sync.Cond
	connectionFailed bool
	lineWasSeized    bool
	hangupCycle      *float64
	remoteEOF        bool
	eofCycle         *float64
	romGen           uint64
	lastCycle        int64
	lastCallGen      int
}

func NewBridge(mu *sync.Mutex, m *emu.Machine) *Bridge {
	b := &Bridge{Mu: mu, M: m, Mode: "off", Host: "127.0.0.1", Port: 16420,
		ReceiveDelayMs: 100, AutoHandshake: true, handshake: "idle",
		romGen: m.ROMGeneration, lastCycle: m.CPU.Cycles,
		lastCallGen: m.Devices.Modem.CallGeneration}
	b.writeCond = sync.NewCond(mu)
	m.OnSerialTx = func(channel int, v byte) { b.onSerialTx(channel, v) }
	return b
}

func (b *Bridge) SetLastCycle(c int64) { b.lastCycle = c }

// State returns the transport status payload served by /api/ncc; field
// order is part of the wire format.
func (b *Bridge) State() StateData {
	return StateData{
		Mode: b.Mode, Host: b.Host, Port: b.Port,
		Armed:                b.Mode == "connect" && b.connState == 0 && !b.connectionFailed,
		Connecting:           b.connState == 1,
		Connected:            b.connState == 2,
		AutoHandshake:        b.AutoHandshake,
		Handshake:            b.handshake,
		Received:             b.Received,
		Transmitted:          b.Transmitted,
		PendingBytes:         len(b.rx),
		PendingTransmitBytes: len(b.tx),
		DrainingRemoteEOF:    b.remoteEOF,
		ReceiveDelayMs:       b.ReceiveDelayMs,
		Error:                b.errStr,
		Protocol:             Protocol,
	}
}

// Configure validates and applies a new target, ending any active session
// first. Call with Mu held.
func (b *Bridge) Configure(body map[string]any) (StateData, error) {
	mode := "connect"
	if v, ok := body["mode"]; ok && v != nil {
		s, isStr := v.(string)
		if !isStr || (s != "off" && s != "connect") {
			return StateData{}, emu.Err("mode must be connect or off")
		}
		mode = s
	}
	host := "127.0.0.1"
	if v, ok := body["host"]; ok && v != nil {
		s, isStr := v.(string)
		if !isStr || len(s) == 0 || len(s) > 253 || isAllSpace(s) {
			return StateData{}, emu.Err("Invalid TCP host")
		}
		host = s
	}
	port := int64(16420)
	if v, ok := body["port"]; ok && v != nil {
		f, isNum := v.(float64)
		if !isNum || f != float64(int64(f)) || f < 1 || f > 65535 {
			return StateData{}, emu.Err("port must be 1..65535")
		}
		port = int64(f)
	}
	autoHandshake := true
	if v, ok := body["autoHandshake"]; ok && v != nil {
		boolVal, isBool := v.(bool)
		if !isBool {
			return StateData{}, emu.Err("autoHandshake must be boolean")
		}
		autoHandshake = boolVal
	}
	receiveDelay := int64(100)
	if v, ok := body["receiveDelayMs"]; ok && v != nil {
		f, isNum := v.(float64)
		if !isNum || f != float64(int64(f)) || f < 0 || f > 2000 {
			return StateData{}, emu.Err("receiveDelayMs must be 0..2000")
		}
		receiveDelay = int64(f)
	}
	b.Close()
	b.Mode = mode
	b.Host = host
	b.Port = int(port)
	b.AutoHandshake = autoHandshake
	b.ReceiveDelayMs = int(receiveDelay)
	b.errStr = nil
	b.connectionFailed = false
	b.romGen = b.M.ROMGeneration
	b.lastCycle = b.M.CPU.Cycles
	b.lastCallGen = b.M.Devices.Modem.CallGeneration
	if mode == "connect" {
		b.M.Devices.Management = "TCP target armed"
	}
	return b.State(), nil
}

func isAllSpace(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// EndSession tears down the call-scoped session: socket close, queue drops,
// carrier/answer-tone clear, management string restore, and a call-ended
// event recording the reason. Call with Mu held.
func (b *Bridge) EndSession(reason string) {
	b.generation++
	hadSocket := b.connState != 0
	conn := b.conn
	b.conn = nil
	b.connState = 0
	if conn != nil {
		conn.Close()
	}
	b.rx = nil
	b.rxReadyAt = nil
	b.tx = nil
	b.writeQ = nil
	b.writePending = 0
	b.writeCond.Broadcast()
	b.M.Devices.AsciRx[1] = emu.ByteList{}
	b.handshake = "idle"
	b.hangupCycle = nil
	b.lineWasSeized = false
	b.remoteEOF = false
	b.eofCycle = nil
	b.connectionFailed = false
	b.M.Devices.Modem.Carrier = false
	b.M.Devices.Modem.AnswerTone = false
	if b.Mode == "connect" {
		b.M.Devices.Management = "TCP target armed"
	} else {
		b.M.Devices.Management = "offline"
	}
	if hadSocket {
		b.M.Event("ncc.tcp.call-ended", emu.Ordered("reason", reason))
	}
}

// Close disables the target (mode off) and ends any session.
func (b *Bridge) Close() {
	b.Mode = "off"
	b.EndSession("target disabled")
}
