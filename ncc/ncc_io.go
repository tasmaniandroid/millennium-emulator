package ncc

import (
	"encoding/hex"
	"io"
	"net"
	"strconv"
	"time"

	"millennium/emu"
)

func (b *Bridge) onSerialTx(channel int, v byte) {
	if channel != 1 || b.Mode != "connect" {
		return
	}
	b.observeCall()
	if b.connectionFailed {
		return
	}
	// UART housekeeping after a call cannot reach a physically disconnected
	// modem and must not create a new TCP conversation on its own.
	if b.connState == 0 && (b.M.RelayState()&1) == 0 {
		return
	}
	b.tx = append(b.tx, v)
	if (b.M.RelayState() & 1) != 0 {
		b.lineWasSeized = true
	}
	if b.connState == 0 {
		b.openSession()
	}
	b.flushTX()
}

func (b *Bridge) observeCall() {
	generation := b.M.Devices.Modem.CallGeneration
	if generation == b.lastCallGen {
		return
	}
	if b.connState != 0 || len(b.rx) > 0 || len(b.tx) > 0 || b.connectionFailed {
		b.EndSession("fresh C.O. seizure")
	} else {
		b.handshake = "idle"
		b.phaseCycle = 0
	}
	b.lastCallGen = generation
}

func (b *Bridge) flushTX() {
	if b.connState != 2 || len(b.tx) == 0 {
		return
	}
	data := b.tx
	b.tx = nil
	b.writePending += len(data)
	b.writeQ = append(b.writeQ, data)
	b.Transmitted += int64(len(data))
	b.M.Event("ncc.tcp.tx", emu.Ordered("bytes", len(data), "hex", hex.EncodeToString(data)))
	b.writeCond.Broadcast()
}

func (b *Bridge) openSession() {
	if b.Mode != "connect" || b.connState != 0 || b.connectionFailed {
		return
	}
	b.generation++
	gen := b.generation
	b.connState = 1
	b.M.Devices.Management = "TCP connecting"
	b.M.Event("ncc.tcp.connecting", emu.Ordered("host", b.Host, "port", b.Port,
		"bufferedBytes", len(b.tx)))
	addr := net.JoinHostPort(b.Host, strconv.Itoa(b.Port))
	go func() {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		b.Mu.Lock()
		defer b.Mu.Unlock()
		if gen != b.generation {
			if conn != nil {
				conn.Close()
			}
			return
		}
		if err != nil {
			msg := err.Error()
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				msg = "NCC connection timeout"
			}
			b.connectionFailed = true
			b.errStr = &msg
			b.M.Event("ncc.tcp.error", emu.Ordered("message", msg))
			b.connState = 0
			b.M.Devices.Management = "TCP disconnected"
			b.tx = nil
			b.writeQ = nil
			b.writePending = 0
			b.remoteEOF = true
			b.eofCycle = nil
			b.M.Event("ncc.tcp.disconnected", emu.Ordered())
			b.writeCond.Broadcast()
			return
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.SetNoDelay(true)
		}
		b.conn = conn
		b.connState = 2
		b.M.Devices.Management = "TCP connected"
		b.errStr = nil
		b.M.Event("ncc.tcp.connected", emu.Ordered("host", b.Host, "port", b.Port))
		b.flushTX()
		b.startWriter(conn, gen)
		b.startReader(conn, gen)
	}()
}

func (b *Bridge) startWriter(conn net.Conn, gen int) {
	go func() {
		for {
			b.Mu.Lock()
			for len(b.writeQ) == 0 {
				if gen != b.generation || b.conn == nil {
					b.writeQ = nil
					b.writePending = 0
					b.Mu.Unlock()
					return
				}
				b.writeCond.Wait()
			}
			if gen != b.generation || b.conn == nil {
				b.writeQ = nil
				b.writePending = 0
				b.Mu.Unlock()
				return
			}
			chunk := b.writeQ[0]
			b.writeQ = b.writeQ[1:]
			b.Mu.Unlock()
			_, _ = conn.Write(chunk)
			b.Mu.Lock()
			b.writePending -= len(chunk)
			b.Mu.Unlock()
		}
	}()
}

func (b *Bridge) startReader(conn net.Conn, gen int) {
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				b.Mu.Lock()
				if gen != b.generation {
					b.Mu.Unlock()
					return
				}
				// TCP segmentation has no significance; forward the complete byte stream.
				b.Received += int64(n)
				ready := float64(b.M.CPU.Cycles) + b.M.MsCycles(float64(b.ReceiveDelayMs))
				b.rx = append(b.rx, buf[:n]...)
				for i := 0; i < n; i++ {
					b.rxReadyAt = append(b.rxReadyAt, ready)
				}
				b.M.Event("ncc.tcp.rx", emu.Ordered("bytes", n, "hex", hex.EncodeToString(buf[:n])))
				b.Mu.Unlock()
			}
			if err != nil {
				b.Mu.Lock()
				if gen != b.generation {
					b.Mu.Unlock()
					return
				}
				if err != io.EOF {
					msg := err.Error()
					b.connectionFailed = true
					b.errStr = &msg
					b.M.Event("ncc.tcp.error", emu.Ordered("message", msg))
				}
				// On remote close: a manager may send its final bytes and
				// immediately close; keep them until the UART has delivered them.
				b.conn = nil
				b.connState = 0
				b.M.Devices.Management = "TCP disconnected"
				b.connectionFailed = true
				b.tx = nil
				b.writeQ = nil
				b.writePending = 0
				b.remoteEOF = true
				b.eofCycle = nil
				b.M.Event("ncc.tcp.disconnected", emu.Ordered())
				b.writeCond.Broadcast()
				b.Mu.Unlock()
				conn.Close()
				return
			}
		}
	}()
}

// Pump advances transport state: session lifecycle around modem
// seize/release, RX delivery into the ASCI1 queue with simulated latency,
// EOF draining before carrier drop, and the optional analogue handshake
// surrogate. Called from the realtime tick and between execution batches,
// with Mu held.
func (b *Bridge) Pump() {
	m := b.M
	modem := m.Devices.Modem
	// Loading/resetting/restoring the phone ends any external conversation.
	if m.ROMGeneration != b.romGen || m.CPU.Cycles < b.lastCycle {
		b.EndSession("phone reset or image changed")
	}
	b.romGen = m.ROMGeneration
	b.lastCycle = m.CPU.Cycles
	b.observeCall()
	cycles := float64(m.CPU.Cycles)
	seized := (m.RelayState() & 1) != 0
	if seized {
		b.lineWasSeized = true
		b.hangupCycle = nil
	} else if b.lineWasSeized {
		if b.hangupCycle == nil {
			c := cycles
			b.hangupCycle = &c
		}
		// Let UART output and the TCP write queue drain before ending the call.
		if cycles-*b.hangupCycle >= m.MsCycles(200) && len(b.tx) == 0 && b.writePending == 0 {
			b.EndSession("firmware released modem relay")
			return
		}
	}
	if b.Mode == "off" {
		return
	}
	if b.connState == 2 {
		m.Devices.Management = "TCP connected"
	} else if b.connState == 0 && !b.connectionFailed {
		m.Devices.Management = "TCP target armed"
	}
	b.flushTX()
	if len(b.rx) > 0 {
		room := 2048 - len(m.Devices.AsciRx[1])
		if room > 0 {
			limit := room
			if len(b.rx) < limit {
				limit = len(b.rx)
			}
			if limit > 512 {
				limit = 512
			}
			count := 0
			for count < limit && b.rxReadyAt[count] <= cycles {
				count++
			}
			if count > 0 {
				block := append([]byte(nil), b.rx[:count]...)
				b.rx = b.rx[count:]
				b.rxReadyAt = b.rxReadyAt[count:]
				data := emu.NewEventDataRaw(emu.Ordered("channel", 1, "hex", hex.EncodeToString(block)))
				_ = m.DeviceEvent("serial", data, true)
			}
		}
	}
	if b.remoteEOF && len(b.rx) == 0 && len(m.Devices.AsciRx[1]) == 0 {
		if b.eofCycle == nil {
			c := cycles
			b.eofCycle = &c
		}
		// Keep carrier through the firmware's normal clear sequence instead
		// of reporting a false error 23 when a TCP peer closes immediately.
		if cycles-*b.eofCycle >= m.MsCycles(500) {
			modem.Carrier = false
			modem.AnswerTone = false
			b.remoteEOF = false
		}
	}
	// Optional analogue-line surrogate. This does not interpret NCC traffic.
	if !b.AutoHandshake || b.connectionFailed {
		return
	}
	// The local line surrogate must complete before firmware starts ASCI1.
	if modem.Digits == "" || !seized {
		b.handshake = "idle"
		return
	}
	if b.handshake == "idle" && cycles-modem.LastDigitCycle >= m.MsCycles(1000) {
		_ = m.DeviceEvent("line", emu.NewEventDataRaw(emu.Ordered("answerTone", true)), true)
		b.handshake = "answer tone"
		b.phaseCycle = cycles
	} else if b.handshake == "answer tone" && cycles-b.phaseCycle >= m.MsCycles(3000) {
		_ = m.DeviceEvent("line",
			emu.NewEventDataRaw(emu.Ordered("answerTone", false, "carrier", true)), true)
		b.handshake = "carrier"
	}
}
