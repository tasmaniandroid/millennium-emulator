package ws

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// maskedFrame builds a client-to-server frame (mask bit set), matching
// what a browser WebSocket sends.
func maskedFrame(opcode byte, payload []byte) []byte {
	n := len(payload)
	var hdr []byte
	switch {
	case n < 126:
		hdr = []byte{0x80 | opcode, byte(n) | 0x80}
	case n <= 65535:
		hdr = []byte{0x80 | opcode, 126 | 0x80, 0, 0}
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[0] = 0x80 | opcode
		hdr[1] = 127 | 0x80
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	key := []byte{0xA5, 0xA5, 0xA5, 0xA5}
	hdr = append(hdr, key...)
	out := append(hdr, payload...)
	for i := range out[len(hdr):] {
		out[len(hdr)+i] ^= key[i&3]
	}
	return out
}

func TestFrameEncoding(t *testing.T) {
	small := frame(1, []byte("hi"))
	if small[0] != 0x81 || small[1] != 2 || string(small[2:]) != "hi" {
		t.Fatalf("small=%v", small)
	}
	mid := frame(1, bytes.Repeat([]byte{0x41}, 300))
	if mid[1] != 126 || binary.BigEndian.Uint16(mid[2:4]) != 300 {
		t.Fatalf("mid header=%v", mid[:4])
	}
	big := frame(1, bytes.Repeat([]byte{0x41}, 70000))
	if big[1] != 127 || binary.BigEndian.Uint64(big[2:10]) != 70000 {
		t.Fatalf("big header=%v", big[:10])
	}
}

func TestPeerReceivesMaskedText(t *testing.T) {
	client, srv := net.Pipe()
	defer client.Close()
	got := make(chan string, 1)
	p := NewPeer(srv, func(s string) { got <- s }, nil)
	defer p.Close(1000, "")

	go func() { _, _ = client.Write(maskedFrame(1, []byte(`{"a":1}`))) }()
	select {
	case s := <-got:
		if s != `{"a":1}` {
			t.Fatalf("got %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message delivered")
	}
}

func TestPeerRejectsUnmaskedFrame(t *testing.T) {
	client, srv := net.Pipe()
	defer client.Close()
	p := NewPeer(srv, nil, nil)
	defer p.Close(1000, "")

	bad := frame(1, []byte("no mask"))
	go func() { _, _ = client.Write(bad) }()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	closeFrame := buf[:n]
	if closeFrame[0]&0x0f != 8 {
		t.Fatalf("expected close frame, got opcode %d", closeFrame[0]&0x0f)
	}
	code := binary.BigEndian.Uint16(closeFrame[2:4])
	if code != 1002 {
		t.Fatalf("close code=%d", code)
	}
}

func TestPeerAnswersPingWithPong(t *testing.T) {
	client, srv := net.Pipe()
	defer client.Close()
	p := NewPeer(srv, nil, nil)
	defer p.Close(1000, "")

	go func() { _, _ = client.Write(maskedFrame(9, []byte("hb"))) }()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	pong := buf[:n]
	if pong[0]&0x0f != 10 {
		t.Fatalf("expected pong, got opcode %d", pong[0]&0x0f)
	}
	if string(pong[2:n]) != "hb" {
		t.Fatalf("pong payload=%q", pong[2:n])
	}
}

func TestSendReportsClosedPeer(t *testing.T) {
	client, srv := net.Pipe()
	p := NewPeer(srv, nil, nil)
	p.Close(1000, "bye")
	client.Close()
	time.Sleep(50 * time.Millisecond)
	// After the close frame drains and the socket dies, Send must report false.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !p.Send([]byte(`{"x":1}`), false) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Send kept reporting success on a closed peer")
}
