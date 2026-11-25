package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type apiTest struct {
	t    *testing.T
	srv  *httptest.Server
	app  *App
	root string
	bin  string // absolute path of TEST.BIN
}

func newAPI(t *testing.T) *apiTest {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "TEST.BIN")
	if err := os.WriteFile(bin, []byte{0x3e, 0, 0x3c, 0xc3, 0x02, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	app := CreateApp()
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	a := &apiTest{t: t, srv: srv, app: app, root: root, bin: bin}
	status, data := a.post("/api/attach", map[string]any{"path": root})
	if status != 200 {
		t.Fatalf("attach: %d %v", status, data)
	}
	return a
}

func (a *apiTest) do(method, route string, body any, headers map[string]string) (int, []byte) {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.srv.URL+route, rdr)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, data
}

func (a *apiTest) get(route string) (int, map[string]any) {
	status, raw := a.do("GET", route, nil, nil)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return status, out
}

func (a *apiTest) post(route string, body any) (int, map[string]any) {
	status, raw := a.do("POST", route, body, nil)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return status, out
}

func TestAPIAttachDetachDiscovery(t *testing.T) {
	a := newAPI(t)
	// idempotent re-attach
	status, data := a.post("/api/attach", map[string]any{"path": a.root})
	if status != 200 {
		t.Fatalf("re-attach: %d %v", status, data)
	}
	status, raw := a.do("GET", "/api/attach", nil, nil)
	var sources []Source
	_ = json.Unmarshal(raw, &sources)
	if status != 200 || len(sources) != 1 || sources[0].Kind != "directory" || sources[0].Images != 1 {
		t.Fatalf("sources: %d %s", status, raw)
	}
	status, raw = a.do("GET", "/api/firmwares", nil, nil)
	var list []firmwareEntry
	_ = json.Unmarshal(raw, &list)
	if status != 200 || len(list) != 1 || list[0].Path != a.bin {
		t.Fatalf("firmwares: %d %s", status, raw)
	}
	// missing path rejected
	if status, data := a.post("/api/attach", map[string]any{"path": filepath.Join(a.root, "nope")}); status != 400 {
		t.Fatalf("attach missing: %d %v", status, data)
	}
	if status, data := a.post("/api/attach", map[string]any{}); status != 400 || data["error"] != "path is required" {
		t.Fatalf("attach no path: %d %v", status, data)
	}
	// detach by id, then firmwares is empty
	if status, _ := a.post("/api/detach", map[string]any{"id": sources[0].ID}); status != 200 {
		t.Fatalf("detach: %d", status)
	}
	status, raw = a.do("GET", "/api/firmwares", nil, nil)
	_ = json.Unmarshal(raw, &list)
	if status != 200 || len(list) != 0 {
		t.Fatalf("firmwares after detach: %d %s", status, raw)
	}
	if status, data := a.post("/api/detach", map[string]any{"id": 999}); status != 400 || data["error"] != "Source is not attached" {
		t.Fatalf("detach missing: %d %v", status, data)
	}
	// loading an unattached absolute path still works
	if status, data := a.post("/api/load", map[string]any{"path": a.bin}); status != 200 {
		t.Fatalf("load unattached: %d %v", status, data)
	}
}

func TestAPIBootAndRun(t *testing.T) {
	a := newAPI(t)
	status, data := a.post("/api/load", map[string]any{"path": a.bin})
	if status != 200 {
		t.Fatalf("load: %d %v", status, data)
	}
	if data["firmware"].(map[string]any)["path"] != a.bin {
		t.Fatalf("firmware=%v", data["firmware"])
	}
	status, data = a.post("/api/run", map[string]any{"instructions": 1000})
	if status != 200 {
		t.Fatalf("run: %d %v", status, data)
	}
	exec := data["execution"].(map[string]any)
	if exec["steps"].(float64) != 1000 || exec["reason"] != "budget" {
		t.Fatalf("execution=%v", exec)
	}
	status, state := a.get("/api/state")
	if status != 200 || state["cpu"] == nil || state["realtime"] == nil || state["view"] == nil {
		t.Fatalf("state: %d %v", status, state)
	}
}

func TestAPIErrorShapes(t *testing.T) {
	a := newAPI(t)
	status, data := a.post("/api/run", map[string]any{"instructions": 10})
	if status != 400 || data["error"] != "Load firmware first" {
		t.Fatalf("run unloaded: %d %v", status, data)
	}
	status, data = a.get("/api/nope")
	if status != 404 || data["error"] != "Unknown API endpoint" {
		t.Fatalf("unknown api: %d %v", status, data)
	}
	status, raw := a.do("GET", "/missing-page", nil, nil)
	var page map[string]any
	_ = json.Unmarshal(raw, &page)
	if status != 404 || page["error"] != "Not found" {
		t.Fatalf("static 404: %d %v", status, page)
	}
	a.post("/api/load", map[string]any{"path": a.bin})
	status, data = a.post("/api/speed", map[string]any{"factor": "x"})
	if status != 400 || data["error"] != "factor must be a number from 0.1 to 64" {
		t.Fatalf("speed: %d %v", status, data)
	}
	status, data = a.post("/api/devices", map[string]any{"type": "key", "data": map[string]any{"key": "Q"}})
	if status != 400 || data["error"] != "Unknown key" {
		t.Fatalf("bad key: %d %v", status, data)
	}
	status, data = a.post("/api/memory", map[string]any{"space": "logical", "address": 0, "hex": "ff"})
	if status != 400 || data["error"] != "ROM writes require /api/patches with expected bytes" {
		t.Fatalf("rom write: %d %v", status, data)
	}
	status, _ = a.post("/api/load", map[string]any{"path": filepath.Join(a.root, "absent.bin")})
	if status != 400 {
		t.Fatalf("load missing accepted: %d", status)
	}
	status, data = a.post("/api/load", map[string]any{"path": a.root})
	if status != 400 || data["error"] != "Expected a firmware image file, not a directory" {
		t.Fatalf("load dir: %d %v", status, data)
	}
}

func TestAPIOriginAndStatic(t *testing.T) {
	a := newAPI(t)
	status, data := a.do("GET", "/api/state", nil, map[string]string{"Origin": "http://evil.example"})
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	if status != 403 || out["error"] != "Cross-origin requests are disabled" {
		t.Fatalf("origin: %d %v", status, out)
	}
	status, raw := a.do("GET", "/", nil, nil)
	if status != 200 || !bytes.Contains(raw, []byte("<html")) {
		t.Fatalf("index: %d", status)
	}
	status, raw = a.do("GET", "/openapi.json", nil, nil)
	var spec map[string]any
	_ = json.Unmarshal(raw, &spec)
	if status != 200 || spec["openapi"] != "3.1.0" {
		t.Fatalf("openapi: %d", status)
	}
}

func TestAPISnapshotPatchFlow(t *testing.T) {
	a := newAPI(t)
	a.post("/api/load", map[string]any{"path": a.bin})
	a.post("/api/run", map[string]any{"instructions": 100})
	status, snap := a.post("/api/snapshots", map[string]any{"name": "s1"})
	if status != 200 || snap["name"] != "s1" {
		t.Fatalf("save: %d %v", status, snap)
	}
	a.post("/api/run", map[string]any{"instructions": 500})
	status, _ = a.post("/api/snapshots/restore", map[string]any{"name": "s1"})
	if status != 200 {
		t.Fatal("restore failed")
	}
	_, state := a.get("/api/state")
	if state["stopReason"] != "snapshot restored" {
		t.Fatalf("stopReason=%v", state["stopReason"])
	}
	status, patch := a.post("/api/patches", map[string]any{"offset": 0, "expected": "3e", "hex": "00"})
	if status != 200 || patch["id"].(float64) != 1 {
		t.Fatalf("patch: %d %v", status, patch)
	}
	status, _ = a.post("/api/patches/revert", nil)
	if status != 200 {
		t.Fatal("revert failed")
	}
	status, raw := a.do("GET", "/api/firmware/export", nil, nil)
	if status != 200 || !bytes.Equal(raw, []byte{0x3e, 0, 0x3c, 0xc3, 0x02, 0}) {
		t.Fatalf("export: %d %v", status, raw)
	}
}

func TestAPIUpload(t *testing.T) {
	a := newAPI(t)
	name := fmt.Sprintf("up-%d.bin", time.Now().UnixNano())
	status, up := a.post("/api/firmwares", map[string]any{"name": name, "hex": "3e00"})
	if status != 200 || up["size"].(float64) != 2 {
		t.Fatalf("upload: %d %v", status, up)
	}
	uploaded, _ := up["path"].(string)
	if !filepath.IsAbs(uploaded) || !strings.HasSuffix(uploaded, name) {
		t.Fatalf("upload path=%v", up["path"])
	}
	t.Cleanup(func() { os.Remove(uploaded) })
	// uploads dir was auto-attached and the image is discoverable + loadable
	status, raw := a.do("GET", "/api/firmwares", nil, nil)
	found := strings.Contains(string(raw), name)
	if status != 200 || !found {
		t.Fatalf("uploaded image not listed: %d", status)
	}
	status, data := a.post("/api/load", map[string]any{"path": uploaded})
	if status != 200 {
		t.Fatalf("load uploaded: %d %v", status, data)
	}
	status, dup := a.post("/api/firmwares", map[string]any{"name": name, "hex": "3e00"})
	if status != 400 || dup["error"] != name+" already exists; existing firmware files are never overwritten" {
		t.Fatalf("dup upload: %d %v", status, dup)
	}
}
