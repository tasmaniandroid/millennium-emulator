"""Python bindings for the Millennium firmware emulator (Go server).

Drives the emulator's HTTP control API. A Phone instance can attach to a
running server or spawn its own (default), giving script-local phones:

    from millennium import Phone

    with Phone() as phone:                      # spawns millennium.exe
        phone.load(r"C:\\firmware\\TEST.BIN")
        phone.run_ms(1_000)
        print(phone.display)

Standard library only. All API errors raise MillenniumError with the
server's verbatim message; execution faults raise ExecutionFault.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import socket
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

__all__ = ["Phone", "MillenniumError", "ExecutionFault", "TimeoutError"]

DEFAULT_EXE_CANDIDATES = ("millennium.exe", "millennium")


class MillenniumError(Exception):
    """An API-level error; the message matches the server's {error} string."""


class ExecutionFault(MillenniumError):
    """A bounded run stopped on a CPU fault (illegal opcode, device error)."""

    def __init__(self, fault: dict, execution: dict):
        super().__init__(fault.get("message", "execution fault"))
        self.fault = fault
        self.execution = execution


class TimeoutError(MillenniumError):
    """A wait_for_display/wait_for_condition budget was exhausted."""

    def __init__(self, message: str, display):
        super().__init__(message)
        self.display = display


def _find_exe(explicit: str | None) -> str:
    if explicit:
        return explicit
    env = os.environ.get("MILLENNIUM_EXE")
    if env:
        return env
    here = Path(__file__).resolve().parent
    # Look in the project root when running from a checkout.
    for candidate in (here.parent.parent / "millennium.exe", here.parent.parent / "millennium"):
        if candidate.exists():
            return str(candidate)
    for name in DEFAULT_EXE_CANDIDATES:
        found = shutil.which(name)
        if found:
            return found
    raise MillenniumError(
        "millennium executable not found; pass exe=... or set MILLENNIUM_EXE")


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Phone:
    """Client for one emulator instance.

    Attach mode:   Phone(base_url="http://127.0.0.1:8787")
    Spawn mode:    Phone()  /  Phone(exe=..., attach=..., env={...})
    """

    def __init__(self, base_url: str | None = None, *, exe: str | None = None,
                 attach: str | list | None = None, session: str | None = None,
                 run: bool = False, env: dict | None = None,
                 timeout: float = 120.0, startup_timeout: float = 15.0):
        self.timeout = timeout
        self._proc: subprocess.Popen | None = None
        if base_url:
            self.base_url = base_url.rstrip("/")
        else:
            port = _free_port()
            command = [_find_exe(exe), f"--port={port}"]
            if session:
                command.append(f"--session={session}")
            if run:
                command.append("--run")
            child_env = dict(os.environ)
            if env:
                child_env.update({k: str(v) for k, v in env.items()})
            self._proc = subprocess.Popen(
                command, env=child_env,
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
            self.base_url = f"http://127.0.0.1:{port}"
            self._wait_ready(startup_timeout)
        if attach:
            paths = attach if isinstance(attach, (list, tuple)) else [attach]
            for p in paths:
                self.attach(p)

    # --- lifecycle -------------------------------------------------------

    def _wait_ready(self, startup_timeout: float) -> None:
        deadline = time.monotonic() + startup_timeout
        while time.monotonic() < deadline:
            if self._proc and self._proc.poll() is not None:
                err = self._proc.stderr.read() if self._proc.stderr else ""
                raise MillenniumError(f"emulator exited during startup: {err.strip()}")
            try:
                self.get("state")
                return
            except Exception:
                time.sleep(0.05)
        raise MillenniumError("emulator did not become ready in time")

    def close(self) -> None:
        if self._proc and self._proc.poll() is None:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self._proc.kill()
        self._proc = None

    def __enter__(self) -> "Phone":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    # --- raw HTTP --------------------------------------------------------

    def get(self, route: str, **query) -> dict:
        url = f"{self.base_url}/api/{route}"
        if query:
            clean = {k: v for k, v in query.items() if v is not None}
            if clean:
                url += "?" + urllib.parse.urlencode(clean)
        return self._request(url, None)

    def post(self, route: str, body: dict | None = None) -> dict:
        url = f"{self.base_url}/api/{route}"
        data = json.dumps(body if body is not None else {}).encode()
        return self._request(url, data)

    def _request(self, url: str, data: bytes | None) -> dict:
        headers = {"Content-Type": "application/json"} if data is not None else {}
        req = urllib.request.Request(url, data=data, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as res:
                payload = res.read()
                ctype = res.headers.get("Content-Type", "")
                if "json" not in ctype:
                    return payload  # binary exports
                return json.loads(payload)
        except urllib.error.HTTPError as e:
            payload = e.read()
            try:
                raise MillenniumError(json.loads(payload).get("error", payload.decode(errors="replace")))
            except json.JSONDecodeError:
                raise MillenniumError(f"HTTP {e.code}: {payload.decode(errors='replace')}")

    def get_binary(self, route: str) -> bytes:
        return self.get(route)  # returns raw bytes for octet-stream routes

    # --- machine state ---------------------------------------------------

    @property
    def state(self) -> dict:
        return self.get("state")

    @property
    def display(self) -> list:
        return self.state["display"]

    @property
    def display_text(self) -> str:
        return " | ".join(self.display)

    @property
    def cycles(self) -> int:
        return self.state["cpu"]["cycles"]

    def speed(self, factor: float) -> dict:
        return self.post("speed", {"factor": factor})

    # --- execution -------------------------------------------------------

    def load(self, path: str, board: str = "auto", map_path: str | None = None) -> dict:
        body = {"path": str(path), "board": board}
        if map_path:
            body["mapPath"] = str(map_path)
        return self.post("load", body)

    # --- runtime firmware attachment ---------------------------------------

    def attach(self, path, name: str | None = None) -> dict:
        """Attach an absolute file or directory (scanned for .bin) as a
        firmware source. Idempotent for the same resolved path."""
        body = {"path": str(path)}
        if name:
            body["name"] = name
        return self.post("attach", body)

    def detach(self, id: int | None = None, path=None) -> list:
        body = {}
        if id is not None:
            body["id"] = id
        if path is not None:
            body["path"] = str(path)
        return self.post("detach", body)

    def sources(self) -> list:
        return self.get("attach")

    def firmwares(self) -> list:
        """All .bin images across attached sources: [{path, size}]."""
        return self.get("firmwares")

    def reset(self, cold: bool = False) -> dict:
        return self.post("reset", {"cold": cold})

    def pause(self) -> dict:
        return self.post("pause")

    def resume(self) -> dict:
        return self.post("resume")

    MAX_INSTRUCTIONS_PER_CALL = 5_000_000

    def run(self, instructions: int = 100_000, cycles: int | None = None,
            ignore_breakpoints: bool = False, raise_on_fault: bool = True) -> dict:
        """Bounded execution. Budgets above the API's 5M per-call maximum are
        transparently split into chunks; a non-budget stop (breakpoint,
        watchpoint, fault) ends the run early, exactly like a single call."""
        if cycles is not None or instructions <= self.MAX_INSTRUCTIONS_PER_CALL:
            return self._run_once(instructions, cycles, ignore_breakpoints, raise_on_fault)
        total = {"steps": 0, "instructions": 0, "cycles": 0,
                 "reason": "budget", "hit": None, "fault": None}
        remaining = instructions
        while remaining > 0:
            chunk = min(remaining, self.MAX_INSTRUCTIONS_PER_CALL)
            result = self._run_once(chunk, None, ignore_breakpoints, raise_on_fault)
            total["steps"] += result["steps"]
            total["instructions"] += result["instructions"]
            total["cycles"] += result["cycles"]
            total["reason"] = result["reason"]
            total["hit"] = result["hit"]
            total["fault"] = result["fault"]
            if result["reason"] != "budget":
                return result
            remaining -= chunk
        return total

    def _run_once(self, instructions: int, cycles: int | None,
                  ignore_breakpoints: bool, raise_on_fault: bool) -> dict:
        body = {"instructions": instructions}
        if cycles is not None:
            body["cycles"] = cycles
        route = "step" if ignore_breakpoints else "run"
        result = self.post(route, body)
        execution = result["execution"]
        if raise_on_fault and execution.get("fault"):
            raise ExecutionFault(execution["fault"], execution)
        return execution

    def step(self, instructions: int = 1) -> dict:
        return self.run(instructions, ignore_breakpoints=True)

    MAX_CYCLES_PER_CALL = 100_000_000

    def run_ms(self, ms: float, chunk_instructions: int = 1_000_000) -> None:
        """Execute for `ms` milliseconds of SIMULATED time (the pacing the
        firmware expects; mirrors the Node tools' go(m, ms) helper)."""
        state = self.state
        clock_hz = state["board"]["clockHz"]
        now = state["cpu"]["cycles"]
        target = now + round(ms * clock_hz / 1000)
        while now < target:
            result = self.run(chunk_instructions,
                              cycles=min(target - now, self.MAX_CYCLES_PER_CALL))
            now += result["cycles"]
            if result["reason"] != "budget":
                return

    def scenario(self, steps: list) -> dict:
        return self.post("scenario", {"steps": steps})

    # --- memory / debug --------------------------------------------------

    def read_memory(self, address: int, length: int = 256, space: str = "logical") -> bytes:
        data = self.get("memory", space=space, address=address, length=length)
        return bytes.fromhex(data["hex"])

    def write_memory(self, address: int, data: bytes, space: str = "logical") -> dict:
        return self.post("memory", {"space": space, "address": address, "hex": data.hex()})

    def disassembly(self, address: int | None = None, count: int = 24) -> list:
        return self.get("disassembly", address=address, count=count)

    def symbols(self, search: str | None = None) -> list:
        return self.get("symbols", search=search)

    def set_registers(self, **regs) -> dict:
        return self.post("registers", regs)

    def read_ports(self) -> bytes:
        return bytes.fromhex(self.get("ports")["hex"])

    def write_port(self, port: int, value: int) -> dict:
        return self.post("ports", {"port": port, "value": value})

    def add_breakpoint(self, address: int, space: str = "physical", bbr: int | None = None) -> list:
        body = {"space": space, "address": address}
        if bbr is not None:
            body["bbr"] = bbr
        return self.post("breakpoints", body)

    def clear_breakpoints(self) -> list:
        return self.post("breakpoints", {"clear": True})

    def add_watchpoint(self, address: int, kind: str = "write", space: str = "physical",
                       length: int = 1) -> list:
        return self.post("watchpoints", {"kind": kind, "space": space,
                                         "address": address, "length": length})

    def clear_watchpoints(self) -> list:
        return self.post("watchpoints", {"clear": True})

    def trace(self, enabled: bool | None = None, limit: int | None = None,
              clear: bool = None) -> dict:
        body = {}
        if enabled is not None:
            body["enabled"] = enabled
        if limit is not None:
            body["limit"] = limit
        if clear is not None:
            body["clear"] = clear
        return self.post("trace", body)

    def trace_rows(self) -> list:
        return self.get("trace")

    def events(self, since: int = 0) -> list:
        return self.get("events", since=since)

    def inputs(self) -> list:
        return self.get("inputs")

    def replay(self, events: list) -> dict:
        return self.post("replay", {"events": events})

    # --- snapshots / patches ----------------------------------------------

    def snapshot_save(self, name: str = "default") -> dict:
        return self.post("snapshots", {"name": name})

    def snapshot_restore(self, name: str = "default") -> dict:
        return self.post("snapshots/restore", {"name": name})

    def snapshot_list(self) -> list:
        return self.get("snapshots")

    def snapshot_export(self) -> dict:
        return self.get("snapshot")

    def snapshot_import(self, snapshot: dict) -> dict:
        return self.post("snapshot", snapshot)

    def patch(self, offset: int, expected: bytes | str, new: bytes | str,
              input_sha256: str | None = None) -> dict:
        body = {"offset": offset,
                "expected": expected.hex() if isinstance(expected, bytes) else expected,
                "hex": new.hex() if isinstance(new, bytes) else new}
        if input_sha256:
            body["inputSha256"] = input_sha256
        return self.post("patches", body)

    def patch_revert(self) -> dict:
        return self.post("patches/revert")

    def patches(self) -> list:
        return self.get("patches")

    def firmware_export(self) -> bytes:
        return self.get_binary("firmware/export")

    def flash_export(self) -> bytes:
        return self.get_binary("flash/export")

    # --- device inputs -----------------------------------------------------

    def device(self, type: str, data: dict | None = None) -> dict:
        return self.post("devices", {"type": type, "data": data or {}})

    def key(self, k: str, run_instructions: int = 500_000) -> None:
        """Press one key ('0'-'9','*','#','language','next','volumeUp',
        'volumeDown', or 'release'), then execute."""
        self.device("key", {"key": k})
        if run_instructions:
            self.run(run_instructions)

    def keys(self, sequence: str, per_key: int = 500_000) -> None:
        for ch in sequence:
            self.key(ch, per_key)

    def handset(self, off_hook: bool, run_instructions: int = 500_000) -> None:
        self.device("handset", {"offHook": off_hook})
        if run_instructions:
            self.run(run_instructions)

    def sensor(self, cover_open: bool | None = None, cashbox_removed: bool | None = None,
               run_instructions: int = 200_000) -> None:
        data = {}
        if cover_open is not None:
            data["coverOpen"] = cover_open
        if cashbox_removed is not None:
            data["cashboxRemoved"] = cashbox_removed
        self.device("sensor", data)
        if run_instructions:
            self.run(run_instructions)

    def open_cover(self, run_instructions: int = 2_000_000) -> None:
        self.sensor(cover_open=True, run_instructions=run_instructions)

    def close_cover(self, run_instructions: int = 2_000_000) -> None:
        self.sensor(cover_open=False, run_instructions=run_instructions)

    def coin(self, index: int, run_instructions: int = 200_000) -> None:
        self.device("coin", {"index": index})
        if run_instructions:
            self.run(run_instructions)

    def card_insert(self, track2: str | None = None, *, kind: str = "magnetic",
                    hex_image: str | None = None, write_protected: bool | None = None,
                    run_instructions: int = 200_000) -> None:
        if kind == "magnetic":
            data = {"action": "insert", "kind": "magnetic",
                    "track2": track2 or ";4012888888=3012101000000000000?"}
        else:
            data = {"action": "insert", "kind": "smart", "hex": hex_image}
            if write_protected is not None:
                data["writeProtected"] = write_protected
        self.device("card", data)
        if run_instructions:
            self.run(run_instructions)

    def card_remove(self, run_instructions: int = 200_000) -> None:
        self.device("card", {"action": "remove"})
        if run_instructions:
            self.run(run_instructions)

    def card_position(self, position: str) -> None:
        self.device("card", {"action": "position", "position": position})

    def cards(self) -> dict:
        return self.get("cards")

    def answer(self, method: str = "both", delay_ms: int = 3000) -> None:
        self.device("answer", {"method": method, "delayMs": delay_ms})

    def dtmf(self, digit: str, duration_ms: int = 200) -> None:
        self.device("dtmf", {"digit": digit, "durationMs": duration_ms})

    def telephony(self, hex_bytes: str) -> None:
        self.device("telephony", {"hex": hex_bytes})

    def serial(self, channel: int, hex_bytes: str) -> None:
        self.device("serial", {"channel": channel, "hex": hex_bytes})

    # --- NCC / management ---------------------------------------------------

    def ncc_state(self) -> dict:
        return self.get("ncc")

    def ncc_configure(self, mode: str = "connect", host: str = "127.0.0.1",
                      port: int = 16420, auto_handshake: bool = True,
                      receive_delay_ms: int = 100) -> dict:
        return self.post("ncc", {"mode": mode, "host": host, "port": port,
                                 "autoHandshake": auto_handshake,
                                 "receiveDelayMs": receive_delay_ms})

    # --- high-level waits ----------------------------------------------------

    def wait_for_display(self, match: str | None = None, *, regex: str | None = None,
                         max_instructions: int = 20_000_000, chunk: int = 100_000,
                         timeout_s: float | None = None, realtime: bool = False,
                         poll_interval: float = 0.25) -> list:
        """Execute until a display line contains `match` (or matches `regex`).

        Default mode runs bounded instruction chunks (deterministic, no wall
        clock). With realtime=True the phone free-runs at its current speed
        factor and display is polled until timeout_s.
        Returns the matching display lines; raises TimeoutError otherwise.
        """
        pattern = re.compile(regex) if regex else None

        def matches(lines) -> bool:
            joined = "".join(lines)
            if pattern:
                return bool(pattern.search(joined))
            return match is not None and match in joined

        if realtime:
            if timeout_s is None:
                timeout_s = 60.0
            self.resume()
            try:
                deadline = time.monotonic() + timeout_s
                while time.monotonic() < deadline:
                    lines = self.display
                    if matches(lines):
                        return lines
                    time.sleep(poll_interval)
                raise TimeoutError(f"display never matched {match or regex!r}", self.display)
            finally:
                self.pause()

        deadline = time.monotonic() + timeout_s if timeout_s else None
        executed = 0
        while executed < max_instructions and (deadline is None or time.monotonic() < deadline):
            lines = self.display
            if matches(lines):
                return lines
            step = min(chunk, max_instructions - executed)
            self.run(step)
            executed += step
        lines = self.display
        if matches(lines):
            return lines
        raise TimeoutError(
            f"display never matched {match or regex!r} after {executed} instructions", lines)

    def craft_entry(self, pin: str = "55555", cover_run: int = 2_000_000,
                    per_key: int = 500_000) -> None:
        """Open the cover and enter the craft PIN sequence (default 55555*)."""
        self.open_cover(run_instructions=cover_run)
        self.keys(pin + "*", per_key=per_key)
