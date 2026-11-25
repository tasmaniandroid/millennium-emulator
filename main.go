// Command millennium runs the Millennium firmware emulator server.
// The server starts with no firmware; attach images at runtime via
// POST /api/attach (or the browser UI) and load them with POST /api/load.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"millennium/emu"
	"millennium/server"
)

func argument(name string) (string, bool) {
	prefix := "--" + name + "="
	for _, v := range os.Args[1:] {
		if strings.HasPrefix(v, prefix) {
			return strings.TrimPrefix(v, prefix), true
		}
	}
	return "", false
}

func hasFlag(name string) bool {
	for _, v := range os.Args[1:] {
		if v == "--"+name {
			return true
		}
	}
	return false
}

func main() {
	app := server.CreateApp()

	portVal := any(float64(8787))
	if v, ok := argument("port"); ok {
		portVal = v
	} else if v := os.Getenv("PORT"); v != "" {
		portVal = v
	}
	port, err := emu.Number(portVal, "PORT", 1, 65535)
	if err != nil {
		log.Fatal(err)
	}

	sessionPath, hasSession := argument("session")
	if !hasSession {
		sessionPath = os.Getenv("MILLENNIUM_SESSION")
		hasSession = sessionPath != ""
	}
	if hasSession {
		firmwarePath, err := app.RestoreSessionFile(sessionPath)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Restored paused session: %s\n", firmwarePath)
	}

	if hasFlag("run") && len(app.Machine.ROM) > 0 {
		app.Mu.Lock()
		app.Machine.Running = true
		app.Machine.StopReason = "budget"
		app.Mu.Unlock()
	}

	stop := make(chan struct{})
	defer close(stop)
	go app.StartTick(stop)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Printf("Millennium emulator: http://%s\nAttach firmware: POST %s/api/attach {\"path\":\"<file-or-directory>\"}\nAPI reference: http://%s/openapi.json\n",
		addr, addr, addr)
	if err := http.ListenAndServe(addr, app); err != nil {
		log.Fatal(err)
	}
}
