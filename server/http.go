package server

import (
	"crypto/sha1"
	"embed"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"millennium/ws"
)

//go:embed openapi.json
var openapiJSON []byte

//go:embed web
var webFS embed.FS

var staticFiles = map[string]string{
	"/":                "index.html",
	"/app.js":          "app.js",
	"/display-view.js": "display-view.js",
	"/socket-api.js":   "socket-api.js",
	"/style.css":       "style.css",
	"/workbench.css":   "workbench.css",
}

var wsKeyRe = regexp.MustCompile(`^[A-Za-z0-9+/]{22}==$`)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func (a *App) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == r.Host && u.Scheme == "http"
}

func (a *App) sendJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func (a *App) sendError(w http.ResponseWriter, status int, msg string) {
	a.sendJSON(w, status, jsonOf(map[string]string{"error": msg}))
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Bind loopback; additionally reject cross-origin browser mutations.
	if !a.originAllowed(r) {
		a.sendError(w, 403, "Cross-origin requests are disabled")
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		a.handleUpgrade(w, r)
		return
	}
	route := r.URL.Path
	if route == "/openapi.json" && r.Method == "GET" {
		a.sendJSON(w, 200, openapiJSON)
		return
	}
	if route == "/api/events/stream" && r.Method == "GET" {
		a.handleSSE(w, r)
		return
	}
	if strings.HasPrefix(route, "/api/") {
		var rawBody []byte
		if r.Method == "POST" {
			body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024+1))
			if err != nil {
				a.sendError(w, 400, err.Error())
				return
			}
			if len(body) > 4*1024*1024 {
				a.sendError(w, 400, "Request too large")
				return
			}
			rawBody = body
		}
		q := map[string]string{}
		for k, vs := range r.URL.Query() {
			if len(vs) > 0 {
				q[k] = vs[0]
			}
		}
		a.Mu.Lock()
		resp, err := a.HandleAPI(r.Method, route, q, rawBody)
		a.Mu.Unlock()
		if err != nil {
			a.sendError(w, 400, err.Error())
			return
		}
		if resp.Binary != nil {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="`+resp.BinaryName+`"`)
			w.WriteHeader(resp.Status)
			_, _ = w.Write(resp.Binary)
			return
		}
		a.sendJSON(w, resp.Status, resp.JSON)
		return
	}
	file, ok := staticFiles[route]
	if r.Method != "GET" || !ok {
		a.sendError(w, 404, "Not found")
		return
	}
	content, err := webFS.ReadFile("web/" + file)
	if err != nil {
		a.sendError(w, 404, "Not found")
		return
	}
	ct := "text/html; charset=utf-8"
	if strings.HasSuffix(route, ".js") {
		ct = "text/javascript"
	} else if strings.HasSuffix(route, ".css") {
		ct = "text/css"
	} else if strings.HasSuffix(route, ".json") {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(content)
}

func (a *App) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.sendError(w, 500, "Streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	client := &sseClient{closed: make(chan struct{})}
	client.write = func(event string, data []byte) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}
	a.Mu.Lock()
	initial := jsonOf(a.Result())
	a.clients[client] = true
	client.display = r.URL.Query().Get("display") == "1"
	a.Mu.Unlock()
	client.write("state", initial)
	<-r.Context().Done()
	a.Mu.Lock()
	delete(a.clients, client)
	a.Mu.Unlock()
}

// prefixConn replays hijack-buffered bytes before the raw connection.
type prefixConn struct {
	net.Conn
	pre []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.pre) > 0 {
		n := copy(p, c.pre)
		c.pre = c.pre[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func (a *App) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}
	rawFail := func(status string) {
		_, _ = bufrw.WriteString(status)
		_ = bufrw.Flush()
		_ = conn.Close()
	}
	a.Mu.Lock()
	tooMany := len(a.peers) >= 16
	a.Mu.Unlock()
	if r.URL.RequestURI() != "/ws" || !a.originAllowed(r) || tooMany {
		rawFail("HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n")
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	version := r.Header.Get("Sec-WebSocket-Version")
	rawKey, keyErr := base64.StdEncoding.DecodeString(key)
	if r.Method != "GET" || version != "13" || !wsKeyRe.MatchString(key) || keyErr != nil || len(rawKey) != 16 {
		rawFail("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n")
		return
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	_, _ = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	_ = bufrw.Flush()
	var wrapped net.Conn = conn
	if buffered := bufrw.Reader.Buffered(); buffered > 0 {
		pre, _ := bufrw.Reader.Peek(buffered)
		wrapped = &prefixConn{Conn: conn, pre: append([]byte(nil), pre...)}
	}
	var peer *ws.Peer
	peer = ws.NewPeer(wrapped,
		func(text string) { a.handleWSMessage(peer, text) },
		func() {
			a.Mu.Lock()
			delete(a.peers, peer)
			a.Mu.Unlock()
		})
	a.registerPeer(peer)
}
