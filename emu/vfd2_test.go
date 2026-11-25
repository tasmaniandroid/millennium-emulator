package emu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCursorBlinkOverlayHorizontal(t *testing.T) {
	p := phone()
	p.data(0x15)
	p.text("ABCDEFGHIJKLMNOPQRST")
	p.data(0x14)
	p.data(0x16)
	p.data(19)
	if p.m.Devices.DisplayCursorVisible {
		t.Fatal("cursor visible")
	}
	if len(p.m.Devices.DisplayBlinkPositions) != 1 || p.m.Devices.DisplayBlinkPositions[0] != 19 {
		t.Fatalf("blink=%v", p.m.Devices.DisplayBlinkPositions)
	}
	p.text("U")
	if p.lines()[0] != "BCDEFGHIJKLMNOPQRSTU" {
		t.Fatalf("line=%q", p.lines()[0])
	}
	p.data(0x13)
	p.data(0x17)
	p.text("V")
	if !p.m.Devices.DisplayCursorVisible || p.m.Devices.DisplayMode != 0x15 {
		t.Fatalf("visible=%v mode=%d", p.m.Devices.DisplayCursorVisible, p.m.Devices.DisplayMode)
	}
	if len(p.m.Devices.DisplayBlinkPositions) != 0 {
		t.Fatalf("blink=%v", p.m.Devices.DisplayBlinkPositions)
	}
	if p.lines()[0] != "CDEFGHIJKLMNOPQRSTUV" {
		t.Fatalf("line=%q", p.lines()[0])
	}
}

func TestVerticalModeShift(t *testing.T) {
	p := phone()
	p.data(0x12)
	p.text(strings.Repeat("A", 20) + strings.Repeat("B", 20))
	eqLines(t, p.lines(), [2]string{strings.Repeat("A", 20), strings.Repeat("B", 20)}, "fill")
	if p.m.Devices.Cursor != 39 {
		t.Fatalf("cursor=%d", p.m.Devices.Cursor)
	}
	p.text("C")
	eqLines(t, p.lines(), [2]string{strings.Repeat("B", 20), "C" + strings.Repeat(" ", 19)}, "shift")
	p.text(strings.Repeat("D", 19))
	p.data(0x14)
	p.text("E")
	eqLines(t, p.lines(), [2]string{"C" + strings.Repeat("D", 19), "E" + strings.Repeat(" ", 19)}, "second")
}

func TestFontBytesCannotChangeMode(t *testing.T) {
	p := phone()
	p.data(0x15)
	p.data(0x1b)
	p.data(0xb8)
	bitmap := []byte{0x11, 0x12, 0x15, 0x16, 0x0c}
	for _, b := range bitmap {
		p.data(int(b))
	}
	got := p.m.Devices.DisplayCustomCharacters[0xb8]
	if string(got) != string(bitmap) {
		t.Fatalf("chars=%v", got)
	}
	if p.m.Devices.DisplayMode != 0x15 || p.m.Devices.Cursor != 0 {
		t.Fatalf("mode=%d cursor=%d", p.m.Devices.DisplayMode, p.m.Devices.Cursor)
	}
	p.data(0x1a)
	p.data(0x12)
	p.text("ABC")
	if p.lines()[0] != "ABC"+strings.Repeat(" ", 17) {
		t.Fatalf("line=%q", p.lines()[0])
	}
	if p.m.Devices.DisplayIntensity != 0x12 || p.m.Devices.DisplayMode != 0x15 {
		t.Fatalf("intensity=%d mode=%d", p.m.Devices.DisplayIntensity, p.m.Devices.DisplayMode)
	}
}

func TestClearAndResetRestoreParser(t *testing.T) {
	p := phone()
	p.data(0x15)
	p.text(strings.Repeat("X", 20))
	p.data(12)
	p.text("A")
	if p.lines()[0] != "A"+strings.Repeat(" ", 19) || p.m.Devices.DisplayMode != 0x15 {
		t.Fatalf("line=%q mode=%d", p.lines()[0], p.m.Devices.DisplayMode)
	}
	p.data(0x16)
	p.command(0x40)
	if p.m.Devices.DisplayPending != 0 || p.m.Devices.DisplayMode != 0x11 {
		t.Fatalf("pending=%d mode=%d", p.m.Devices.DisplayPending, p.m.Devices.DisplayMode)
	}
	p.text("@")
	if p.lines()[0][0] != '@' {
		t.Fatalf("line=%q", p.lines()[0])
	}
	p.data(0x15)
	p.data(0x1b)
	p.data(0xb8)
	_ = p.m.Output(0x41, 0)
	if p.m.Devices.DisplayMode != 0x11 || p.m.Devices.CustomRemaining != 0 {
		t.Fatalf("mode=%d remaining=%d", p.m.Devices.DisplayMode, p.m.Devices.CustomRemaining)
	}
	eqLines(t, p.lines(), [2]string{strings.Repeat(" ", 20), strings.Repeat(" ", 20)}, "blank")
}

func TestSnapshotPreservesPendingScrollAndFont(t *testing.T) {
	p := phone()
	p.data(0x15)
	p.command(39)
	p.text("A")
	saved, _ := p.m.Snapshot()
	p.text("B")
	expected := p.lines()
	if err := p.m.Restore(saved); err != nil {
		t.Fatal(err)
	}
	p.text("B")
	eqLines(t, p.lines(), expected, "restored scroll")
	if p.lines()[1] != strings.Repeat(" ", 18)+"AB" {
		t.Fatalf("line2=%q", p.lines()[1])
	}
	p.data(0x1b)
	p.data(0xb8)
	p.data(1)
	p.data(2)
	font, _ := p.m.Snapshot()
	p.data(3)
	p.data(4)
	p.data(5)
	if err := p.m.Restore(font); err != nil {
		t.Fatal(err)
	}
	p.data(3)
	p.data(4)
	p.data(5)
	got := p.m.Devices.DisplayCustomCharacters[0xb8]
	if string(got) != string([]byte{1, 2, 3, 4, 5}) {
		t.Fatalf("chars=%v", got)
	}
}

func TestLegacySnapshotMigration(t *testing.T) {
	p := phone()
	p.text("SAVED TEXT")
	saved, _ := p.m.Snapshot()
	var snap map[string]any
	_ = json.Unmarshal(saved, &snap)
	devs := snap["devices"].(map[string]any)
	// Delete every vfdState key except display/cursor/displayMode/
	// displayPending/customRemaining.
	for _, k := range []string{"displayCursorVisible", "displayScrollPending", "displayBlinkPositions",
		"displayIntensity", "displayRevision", "displayCustomCode", "displayCustomData",
		"displayCustomCharacters"} {
		delete(devs, k)
	}
	devs["displayMode"] = float64(0x14)
	raw, _ := json.Marshal(snap)
	if err := p.m.Restore(raw); err != nil {
		t.Fatal(err)
	}
	if p.lines()[0] != "SAVED TEXT"+strings.Repeat(" ", 10) {
		t.Fatalf("line=%q", p.lines()[0])
	}
	if p.m.Devices.DisplayMode != 0x11 {
		t.Fatalf("mode=%d", p.m.Devices.DisplayMode)
	}
	devs["displayMode"] = float64(0x16)
	devs["customRemaining"] = float64(2)
	raw, _ = json.Marshal(snap)
	if err := p.m.Restore(raw); err != nil {
		t.Fatal(err)
	}
	p.data(19)
	p.text("Q")
	if p.lines()[0] != "SAVED TEXTQ"+strings.Repeat(" ", 9) {
		t.Fatalf("line=%q", p.lines()[0])
	}
}
