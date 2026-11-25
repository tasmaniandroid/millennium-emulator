package server

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"millennium/emu"
)

var nonWordRe = regexp.MustCompile(`[^\w.-]+`)
var binNameRe = regexp.MustCompile(`(?i)^[A-Za-z0-9._-]{1,64}\.bin$`)

type uploadResponse struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	Sha256 string `json:"sha256"`
}

type executionResponse struct {
	Execution emu.RunResult `json:"execution"`
	State     ResultData    `json:"state"`
}

// handlePost1 serves mutation routes (first half). Call with Mu held.
func (a *App) handlePost1(route string, b map[string]any, bRaw map[string]json.RawMessage) (*apiResponse, error) {
	m := a.Machine
	switch route {
	case "/api/firmwares":
		name, ok := b["name"].(string)
		if !ok {
			return nil, emu.Err("name is required")
		}
		base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
		base = nonWordRe.ReplaceAllString(base, "_")
		if !binNameRe.MatchString(base) {
			return nil, emu.Err("name must be a .bin filename of at most 64 characters")
		}
		uploadDir, err := a.UploadDir()
		if err != nil {
			return nil, err
		}
		file := filepath.Join(uploadDir, base)
		if _, err := os.Stat(file); err == nil {
			return nil, emu.Err(base + " already exists; existing firmware files are never overwritten")
		}
		image, err := emu.Bytes(b["hex"])
		if err != nil {
			return nil, err
		}
		if len(image) == 0 || len(image) > 0x100000 {
			return nil, emu.Err("ROM must contain 1..1048576 bytes")
		}
		if err := os.WriteFile(file, image, 0o644); err != nil {
			return nil, err
		}
		return okResp(uploadResponse{Path: file, Size: len(image), Sha256: emu.Hash(image)})
	case "/api/attach":
		src, images, err := a.Attach(b["path"], b["name"])
		if err != nil {
			return nil, err
		}
		return okResp(struct {
			Source *Source         `json:"source"`
			Images []firmwareEntry `json:"images"`
		}{src, images})
	case "/api/detach":
		sources, err := a.Detach(b["id"], b["path"])
		if err != nil {
			return nil, err
		}
		return okResp(sources)
	case "/api/load":
		if err := a.paused(); err != nil {
			return nil, err
		}
		resp, err := a.Load(b["path"], b["mapPath"], b["board"])
		if err != nil {
			return nil, err
		}
		r, err2 := okResp(resp)
		return r, err2
	case "/api/reset":
		if err := a.paused(); err != nil {
			return nil, err
		}
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		a.Ncc.EndSession("phone reset")
		m.Reset(b["cold"] == true)
		r, err := okResp(a.Result())
		return r, err
	case "/api/pause":
		m.Running = false
		m.StopReason = "paused"
		r, err := okResp(a.Result())
		return r, err
	case "/api/resume":
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		m.Running = true
		m.Fault = nil
		r, err := okResp(a.Result())
		return r, err
	case "/api/run", "/api/step":
		if err := a.paused(); err != nil {
			return nil, err
		}
		def := float64(100000)
		ignore := false
		if route == "/api/step" {
			def = 1
			ignore = true
		}
		instrVal, err := emu.Number(orDefault(b["instructions"], def), "instructions", 1, 5000000)
		if err != nil {
			return nil, err
		}
		cycles := int64(math.MaxInt64)
		if v, ok := b["cycles"]; ok {
			cycles, err = emu.Number(v, "cycles", 1, 100000000)
			if err != nil {
				return nil, err
			}
		}
		exec, err := m.Run(emu.RunOptions{Instructions: instrVal, Cycles: cycles, IgnoreBreakpoints: ignore})
		if err != nil {
			return nil, err
		}
		r, err2 := okResp(executionResponse{Execution: exec, State: a.Result()})
		return r, err2
	case "/api/speed":
		rt, err := a.SetSpeed(b["factor"])
		if err != nil {
			return nil, err
		}
		r, err2 := okResp(rt)
		return r, err2
	case "/api/ncc":
		st, err := a.Ncc.Configure(b)
		if err != nil {
			return nil, err
		}
		r, err2 := okResp(st)
		return r, err2
	case "/api/memory":
		if err := a.paused(); err != nil {
			return nil, err
		}
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		s, err := spaceOf(b["space"], "logical")
		if err != nil {
			return nil, err
		}
		maxAddr := int64(0xfffff)
		if s == "logical" {
			maxAddr = 65535
		}
		address, err := emu.Number(b["address"], "address", 0, maxAddr)
		if err != nil {
			return nil, err
		}
		v, err := emu.Bytes(b["hex"])
		if err != nil {
			return nil, err
		}
		limit := int64(0x100000)
		if s == "logical" {
			limit = 65536
		}
		if address+int64(len(v)) > limit {
			return nil, emu.Err("Memory range exceeds address space")
		}
		for i := range v {
			t := address + int64(i)
			if s == "logical" {
				t = int64(m.Physical(int(t)))
			}
			if t < int64(m.Board.RamStart) {
				return nil, emu.Err("ROM writes require /api/patches with expected bytes")
			}
		}
		for i := range v {
			t := address + int64(i)
			if s == "logical" {
				t = int64(m.Physical(int(t)))
			}
			m.WritePhysical(int(t), int(v[i]))
		}
		r, err2 := okResp(struct {
			Written int `json:"written"`
		}{len(v)})
		return r, err2
	case "/api/ports":
		if err := a.paused(); err != nil {
			return nil, err
		}
		if err := a.requireLoaded(); err != nil {
			return nil, err
		}
		port, err := emu.Number(b["port"], "port", 0, 65535)
		if err != nil {
			return nil, err
		}
		value, err := emu.Number(b["value"], "value", 0, 255)
		if err != nil {
			return nil, err
		}
		if err := m.Output(int(port), int(value)); err != nil {
			return nil, err
		}
		r, err2 := okResp(a.Result())
		return r, err2
	}
	return nil, nil
}

func orDefault(v any, def float64) any {
	if v == nil {
		return def
	}
	return v
}
