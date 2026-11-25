// Package ws implements a minimal RFC6455 server-side peer with the frame
// validation, volatile-drop and slow-client semantics the live display
// protocol depends on.
package ws

import (
	"encoding/binary"
	"net"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxMessage = 4 * 1024 * 1024

func frame(opcode byte, payload []byte) []byte {
	length := len(payload)
	var header []byte
	switch {
	case length < 126:
		header = []byte{0x80 | opcode, byte(length)}
	case length <= 65535:
		header = []byte{0x80 | opcode, 126, 0, 0}
		binary.BigEndian.PutUint16(header[2:], uint16(length))
	default:
		header = make([]byte, 10)
		header[0] = 0x80 | opcode
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:], uint64(length))
	}
	return append(header, payload...)
}

// Peer is one WebSocket client connection.
type Peer struct {
	conn      net.Conn
	onMessage func(text string)
	onClose   func()

	mu      sync.Mutex // guards queue state below (not conn I/O)
	closed  bool
	pending int
	queue   [][]byte
	cond    *sync.Cond

	writeMu   sync.Mutex // serializes conn writes
	closeOnce sync.Once
}

func NewPeer(conn net.Conn, onMessage func(string), onClose func()) *Peer {
	p := &Peer{conn: conn, onMessage: onMessage, onClose: onClose}
	p.cond = sync.NewCond(&p.mu)
	go p.writeLoop()
	go p.readLoop()
	return p
}

// Send enqueues a text frame. volatile frames are dropped when the peer's
// send queue exceeds 256 KiB; any peer above 8 MiB is closed with 1013.
func (p *Peer) Send(payload []byte, volatile bool) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	if volatile && p.pending > 256*1024 {
		p.mu.Unlock()
		return false
	}
	if p.pending > 8*1024*1024 {
		p.mu.Unlock()
		p.Close(1013, "Client too slow")
		return false
	}
	f := frame(1, payload)
	p.queue = append(p.queue, f)
	p.pending += len(f)
	p.cond.Broadcast()
	p.mu.Unlock()
	return true
}

// Close sends a close frame and tears the connection down after 1s.
func (p *Peer) Close(code int, reason string) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	data := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(data, uint16(code))
	copy(data[2:], reason)
	if len(reason) > 123 {
		data = data[:2+123]
	}
	f := frame(8, data)
	p.queue = append(p.queue, f)
	p.pending += len(f)
	p.cond.Broadcast()
	p.mu.Unlock()
	time.AfterFunc(time.Second, func() { p.conn.Close() })
}

func (p *Peer) writeLoop() {
	for {
		p.mu.Lock()
		for len(p.queue) == 0 && !p.closed {
			p.cond.Wait()
		}
		if len(p.queue) == 0 && p.closed {
			p.mu.Unlock()
			return
		}
		f := p.queue[0]
		p.queue = p.queue[1:]
		p.pending -= len(f)
		closing := len(f) > 0 && f[0]&0x0f == 8
		p.mu.Unlock()
		p.writeMu.Lock()
		_, err := p.conn.Write(f)
		p.writeMu.Unlock()
		if closing || err != nil {
			if closing {
				// Half-close after the close frame; the 1s teardown timer finishes it.
				if tcp, ok := p.conn.(interface{ CloseWrite() error }); ok {
					tcp.CloseWrite()
				}
			}
			p.mu.Lock()
			p.closed = true
			p.mu.Unlock()
			return
		}
	}
}

// writeDirect is used by the reader for control-frame responses.
func (p *Peer) enqueueControl(f []byte) {
	p.mu.Lock()
	p.queue = append(p.queue, f)
	p.pending += len(f)
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *Peer) markClosed() {
	p.mu.Lock()
	first := !p.closed
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
	if first && p.onClose != nil {
		p.onClose()
	}
}

func (p *Peer) readLoop() {
	buf := []byte{}
	chunk := make([]byte, 64*1024)
	var fragments [][]byte
	fragmentBytes := 0
	fragmenting := false
	for {
		n, err := p.conn.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		stop := false
		for len(buf) >= 2 && !stop {
			b := buf
			fin := b[0]&128 != 0
			opcode := b[0] & 15
			masked := b[1]&128 != 0
			length := int(b[1] & 127)
			offset := 2
			if (b[0]&0x70) != 0 || !masked {
				p.Close(1002, "Invalid client frame")
				stop = true
				break
			}
			if length == 126 {
				if len(b) < 4 {
					break
				}
				length = int(binary.BigEndian.Uint16(b[2:]))
				offset = 4
			} else if length == 127 {
				if len(b) < 10 {
					break
				}
				size := binary.BigEndian.Uint64(b[2:])
				if size > MaxMessage {
					p.Close(1009, "Message too large")
					stop = true
					break
				}
				length = int(size)
				offset = 10
			}
			if length > MaxMessage || (opcode < 8 && fragmentBytes+length > MaxMessage) {
				p.Close(1009, "Message too large")
				stop = true
				break
			}
			if opcode >= 8 && (!fin || length > 125) {
				p.Close(1002, "Invalid control frame")
				stop = true
				break
			}
			if len(b) < offset+4+length {
				break
			}
			mask := b[offset : offset+4]
			payload := make([]byte, length)
			copy(payload, b[offset+4:offset+4+length])
			for i := 0; i < length; i++ {
				payload[i] ^= mask[i&3]
			}
			buf = append([]byte(nil), b[offset+4+length:]...)
			switch {
			case opcode == 8:
				if length == 1 {
					p.Close(1002, "")
					stop = true
					break
				}
				p.mu.Lock()
				p.closed = true
				p.mu.Unlock()
				p.enqueueControl(frame(8, payload))
				stop = true
			case opcode == 9:
				p.enqueueControl(frame(10, payload))
			case opcode == 10:
				// ignore pong
			case opcode == 2:
				p.Close(1003, "JSON text required")
				stop = true
			case opcode != 0 && opcode != 1:
				p.Close(1002, "Invalid opcode")
				stop = true
			case (opcode == 0 && !fragmenting) || (opcode == 1 && fragmenting):
				p.Close(1002, "Invalid continuation")
				stop = true
			default:
				fragments = append(fragments, payload)
				fragmentBytes += length
				fragmenting = !fin
				if fin {
					var complete []byte
					if len(fragments) == 1 {
						complete = fragments[0]
					} else {
						total := 0
						for _, f := range fragments {
							total += len(f)
						}
						complete = make([]byte, 0, total)
						for _, f := range fragments {
							complete = append(complete, f...)
						}
					}
					fragments = nil
					fragmentBytes = 0
					if !utf8.Valid(complete) {
						p.Close(1007, "Invalid UTF-8")
						stop = true
						break
					}
					if p.onMessage != nil {
						p.onMessage(string(complete))
					}
				}
			}
		}
		if stop {
			// Protocol close: frames are queued; the 1s destroy timer in
			// Close() tears the socket down.
			p.markClosed()
			return
		}
		if err != nil {
			p.conn.Close()
			p.markClosed()
			return
		}
	}
}
