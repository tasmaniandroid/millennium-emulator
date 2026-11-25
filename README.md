# Millennium firmware emulator

A Go emulator for compiled Millennium payphone firmware, with an embedded
browser UI and a local debugging API. Bring your own `.bin` images.

See [AGENT_API.md](AGENT_API.md) for scripting, `GET /openapi.json` for HTTP
routes, and [PERIPHERALS.md](PERIPHERALS.md) for cards, line tests, and NCC.

## Build and run

Build with Go 1.27. The module declares Go 1.24 as its minimum.

From the project root, start the server with:

```sh
go run .
```

Open `http://127.0.0.1:8787`, choose **Attach…**, then select and load an image.
You can attach a file or a directory (the server scans it recursively for `.bin`
files), or upload an image through the browser. API clients can load a known path
without attaching it. Relative paths resolve against the server's working directory.
Startup overrides: `PORT` and `MILLENNIUM_SESSION`.

To resume a saved session on a different port:

```sh
go run . --port=8790 --session=state.session.json --run
```

## Tests

```sh
go test ./...
```

The Go suite runs without firmware. Pass `-rom` to opt into the advertising
scroll test or CPU benchmark. The scroll test looks for a same-stem `.MAP`
beside the ROM; pass `-rom-map` if the matching map is elsewhere. The ROM isn't
included. Use an absolute path, or a path relative to the `emu/` package directory.

```sh
go test ./emu -run "^TestFirmwareAdvertisingScroll$" -args -rom ../fixtures/TEST.BIN
go test ./emu -run "^$" -bench "^BenchmarkRun2M$" -args -rom ../fixtures/TEST.BIN
```

## Performance (AMD Ryzen 5 3600)

| Board and image | Bounded execution | Realtime loop (64× target) |
|---|---|---|---|
| MTR1 (`TEST.BIN`, 6.144 MHz) | 18.5M instructions/s (30× realtime) | 27.5× achieved |
| MTR2 (`flash.bin`, 12.288 MHz) | 23.1M instructions/s (14× realtime) | 11.8× achieved |

## Layout

| Path | Contents |
|---|---|
| `emu/`, `disasm/` | CPU, boards, peripherals, snapshots, disassembler |
| `server/`, `ws/` | HTTP API, realtime scheduler, WebSocket transport, embedded UI |
| `ncc/` | Call-scoped TCP bridge |
| `python/` | HTTP client, examples, and binding self-test |

## Python bindings (scripted phone testing)

Run `go build .` from the project root so the scripts can start the server.
Then pass each script the ROM it expects, replacing the example paths below.
The examples import the client directly from the checkout; install it with
`python -m pip install -e python` to use it in your own scripts.

```sh
python python/selftest.py fixtures/TEST.BIN
python python/examples/boot_and_display.py fixtures/TEST.BIN
python python/examples/install_mtr2.py fixtures/flash.bin
```

The MTR2 example expects the NLA1X06 image and stops after the C.O. line test;
it doesn't provision the phone through NCC.

## License

The emulator is licensed under version 3 of the GNU General Public License.
See the [full license text](LICENSE).
