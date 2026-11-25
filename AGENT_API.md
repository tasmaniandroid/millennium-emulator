# Scripting the emulator

Use `http://127.0.0.1:8787` for an already running server. The [OpenAPI spec](server/openapi.json), also served at `/openapi.json`, lists routes and request fields. See [PERIPHERALS.md](PERIPHERALS.md) for card, line, and NCC behavior.

## Load and run

After building the server, run `python -m pip install -e python` from the project root. `Phone()` starts a private server; pass `base_url="http://127.0.0.1:8787"` to use the running one instead.

```python
from millennium import Phone

with Phone() as phone:
    phone.load(r"C:\firmware\TEST.BIN", board="mtr1")
    phone.run_ms(1_000)
    print(phone.display)
```

- Load any compiled `.bin` by server-local path (`POST /api/load`); attachment is optional. Use `POST /api/attach` with `{"path":"..."}` to list a file or directory's images in `GET /api/firmwares`.
- Use `GET /api/boards` to choose a profile. `auto` uses a SHA-256-matching `.build.json` sidecar or falls back to image size; check `profileSource` in the load response. A same-stem `.MAP` can supply symbols but does not prove they match the image.
- Pause realtime execution before bounded runs. Advance simulated time with `run_ms()` between device inputs, rather than sleeping on the host. For a firmware-specific flow, see the [MTR2 install example](python/examples/install_mtr2.py).

## Diagnose and reproduce

Check `execution.reason` after a raw `POST /api/run`: `budget` means the requested run ended normally; a breakpoint, watchpoint, or fault can stop it early. Check `/api/state` and `/api/events` before assuming an unexpected result is a firmware bug. On MTR2, use `space=flash` for raw flash offsets and `space=physical` for bus addresses.

Snapshots require the same ROM hash and board. For offline replay, disable the NCC bridge, restore the snapshot, then send recorded `/api/inputs` to `POST /api/replay` as `{"events":[...]}`. Event timestamps are absolute simulated cycles; TCP connections and debugger writes aren't part of the input log.
