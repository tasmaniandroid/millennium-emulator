"""End-to-end validation of the millennium Python bindings.

Spawns the emulator and exercises the bindings with the TEST.BIN fixture.
Run from the project root:

    python python/selftest.py PATH_TO_TEST.BIN
"""

import argparse
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from millennium import Phone, MillenniumError, ExecutionFault, TimeoutError  # noqa: E402

parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
parser.add_argument("rom", type=Path, help="path to the TEST.BIN fixture")
rom = parser.parse_args().rom.resolve()
if not rom.is_file():
    parser.error(f"ROM file not found: {rom}")
TEST_BIN = str(rom)

passed = 0

def ok(label, cond, extra=""):
    global passed
    if not cond:
        print(f"FAIL {label} {extra}")
        sys.exit(1)
    passed += 1
    print(f"ok {label} {extra}")


with Phone(attach=str(rom.parent)) as phone:
    # Attach/discovery API.
    sources = phone.sources()
    ok("attach", len(sources) == 1 and sources[0]["kind"] == "directory", str(sources))
    firmwares = phone.firmwares()
    ok("firmwares listed", any(Path(f["path"]) == rom for f in firmwares),
        f"{len(firmwares)} images")

    # Load and boot.
    info = phone.load(TEST_BIN)
    ok("load", info["firmware"]["path"] == TEST_BIN, f"board={info['board']['id']}")
    execution = phone.run(2_000_000)
    ok("run 2M", execution["reason"] == "budget", f"cycles={execution['cycles']}")

    lines = phone.wait_for_display("out of service", max_instructions=10_000_000, chunk=250_000)
    ok("wait_for_display", True, str(lines))

    # Keys + events.
    before = phone.events()[-1]["id"] if phone.events() else 0
    phone.key("5")
    events = phone.events(since=before)
    types = [e["type"] for e in events]
    ok("key event", "input.key" in types, str(types[:6]))

    # Handset + coin produce device events.
    before = phone.events()[-1]["id"]
    phone.handset(True, run_instructions=200_000)
    phone.coin(0, run_instructions=100_000)
    phone.handset(False, run_instructions=200_000)
    types = [e["type"] for e in phone.events(since=before)]
    ok("handset+coin events", "input.handset" in types and "input.coin" in types, str(types[:8]))

    # Snapshots: save, perturb, restore.
    phone.pause()
    phone.snapshot_save("t1")
    snap_cycles = phone.state["cpu"]["cycles"]
    phone.run(500_000)
    ok("cycles advanced", phone.state["cpu"]["cycles"] > snap_cycles)
    phone.snapshot_restore("t1")
    ok("snapshot restore", phone.state["cpu"]["cycles"] == snap_cycles)

    # Export/import round trip.
    exported = phone.snapshot_export()
    phone.run(100_000)
    phone.snapshot_import(exported)
    ok("snapshot export/import", phone.state["cpu"]["cycles"] == snap_cycles)

    # Patch + revert (no-op patch of the first ROM byte).
    first = phone.read_memory(0, 1, space="physical")
    entry = phone.patch(0, first, first)
    ok("patch", entry["id"] == 1 and entry["inputSha256"] == entry["outputSha256"])
    phone.patch_revert()
    ok("patch revert", phone.patches() == [])

    # Debugger endpoints.
    phone.add_breakpoint(phone.state["cpu"]["pc"], space="physical")
    result = phone.run(10)
    ok("breakpoint hit", result["reason"] == "breakpoint", result["reason"])
    phone.clear_breakpoints()
    rows = phone.disassembly(count=5)
    ok("disassembly", len(rows) == 5 and rows[0]["text"], rows[0]["text"] if rows else "")
    phone.trace(enabled=True, limit=10)
    phone.run(50)
    ok("trace", len(phone.trace_rows()) > 0)
    phone.trace(enabled=False, clear=True)

    # Memory write into RAM (physical >= 0x40000).
    phone.write_memory(0x40100, bytes([0xAB]), space="physical")
    ok("memory write", phone.read_memory(0x40100, 1, space="physical") == b"\xab")

    # Registers.
    phone.set_registers(a=0x42)
    ok("registers", phone.state["cpu"]["a"] == 0x42)

    # Error propagation: run while running must 400.
    phone.resume()
    try:
        phone.run(10)
        ok("paused-only guard", False)
    except MillenniumError as e:
        ok("paused-only guard", "Pause execution" in str(e), str(e))
    phone.pause()

    # Fault/validation propagation: ROM writes via /api/memory are rejected.
    try:
        phone.write_memory(0, bytes([0xFF, 0xFF]), space="logical")
        ok("rom write rejected", False)
    except MillenniumError as e:
        ok("rom write rejected", "patches" in str(e), str(e))

    # Scenario endpoint.
    result = phone.scenario([
        {"event": {"type": "key", "data": {"key": "1"}}},
        {"run": {"instructions": 100000}},
        {"expectDisplay": "out of"},
    ])
    ok("scenario", isinstance(result["results"], list) and len(result["results"]) >= 2)

    # NCC state reachable.
    ok("ncc state", phone.ncc_state()["mode"] in ("off", "connect"))

print(f"ALL OK ({passed} checks)")
