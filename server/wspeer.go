package server

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"millennium/emu"
	"millennium/ws"
)

type envelope struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

func (a *App) registerPeer(p *ws.Peer) {
	a.Mu.Lock()
	st := &peerState{subVisible: true, eventDevices: a.Machine.Devices}
	a.peers[p] = st
	initial := jsonOf(envelope{Type: "state", Data: a.SocketState()})
	a.Mu.Unlock()
	p.Send(initial, false)
}

func (a *App) wsRespond(p *ws.Peer, id any, status int, data json.RawMessage) {
	idJSON, err := json.Marshal(id)
	if err != nil {
		idJSON = []byte("null")
	}
	msg := `{"type":"response","id":` + string(idJSON) + `,"status":` +
		strconv.Itoa(status) + `,"data":` + string(data) + `}`
	p.Send([]byte(msg), false)
}

// handleWSMessage processes one WebSocket client message: subscription
// updates and RPC-style API requests sharing the HTTP route handlers.
// Protocol violations close the peer with the documented status codes.
func (a *App) handleWSMessage(p *ws.Peer, text string) {
	var msgAny any
	if err := json.Unmarshal([]byte(text), &msgAny); err != nil {
		p.Close(1007, "Invalid JSON")
		return
	}
	msg, ok := msgAny.(map[string]any)
	if !ok {
		p.Close(1008, "Invalid message")
		return
	}
	if t, _ := msg["type"].(string); t == "subscribe" {
		visible := true
		if v, isBool := msg["visible"].(bool); isBool && !v {
			visible = false
		}
		debugger := msg["debugger"] == true
		var addr *int
		if msg["address"] != nil {
			n, err := emu.Number(msg["address"], "disassembly address", 0, 65535)
			if err != nil {
				p.Close(1008, err.Error())
				return
			}
			ai := int(n)
			addr = &ai
		}
		a.Mu.Lock()
		if st, found := a.peers[p]; found {
			st.subVisible = visible
			st.subDebugger = debugger
			st.subAddress = addr
			st.lastDebugger = 0
		}
		a.Mu.Unlock()
		return
	}
	id := msg["id"]
	idOK := false
	if f, isNum := id.(float64); isNum {
		idOK = f == float64(int64(f)) && f >= 1 && f <= float64(emu.MaxSafeInteger)
	}
	route, routeIsStr := msg["route"].(string)
	method := "GET"
	methodOK := true
	if v, exists := msg["method"]; exists && v != nil {
		s, isStr := v.(string)
		if !isStr {
			methodOK = false
		} else {
			method = s
		}
	}
	if method != "GET" && method != "POST" {
		methodOK = false
	}
	if t, _ := msg["type"].(string); t != "request" || !idOK || !routeIsStr ||
		!methodOK || len(route) > 2048 {
		p.Close(1008, "Invalid API request")
		return
	}
	full := "/api/" + route
	u, err := url.Parse("http://localhost" + full)
	if err != nil {
		p.Close(1008, "Invalid API request")
		return
	}
	pathname := u.Path
	if !strings.HasPrefix(pathname, "/api/") ||
		pathname == "/api/events/stream" ||
		pathname == "/api/firmware/export" ||
		pathname == "/api/flash/export" {
		a.wsRespond(p, id, 400, jsonOf(map[string]string{"error": "This endpoint requires HTTP"}))
		return
	}
	q := map[string]string{}
	for k, vs := range u.Query() {
		if len(vs) > 0 {
			q[k] = vs[0]
		}
	}
	var rawBody []byte
	if bodyVal, exists := msg["body"]; exists && bodyVal != nil {
		rawBody = jsonOf(bodyVal)
	} else {
		rawBody = []byte("{}")
	}
	a.Mu.Lock()
	resp, apiErr := a.HandleAPI(method, pathname, q, rawBody)
	a.Mu.Unlock()
	if apiErr != nil {
		a.wsRespond(p, id, 400, jsonOf(map[string]string{"error": apiErr.Error()}))
		return
	}
	a.wsRespond(p, id, resp.Status, resp.JSON)
}
