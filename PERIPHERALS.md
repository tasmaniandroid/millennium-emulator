# Emulated peripherals

Use **Phone I/O** in the browser for line signals, cabinet switches, card
inputs, and the NCC connection. If the emulator is paused, advance simulated
time after sending an input so the firmware can see it. In Python, use
`phone.run_ms()` rather than sleeping. See the [API spec](server/openapi.json)
for request fields.

## Line tests

Follow the prompts on the phone display; the installation sequence varies by
ROM. At **C.O. line check / Go offhook**, leave **Line connected** and
**Dial tone available** checked, then lift the handset. `Completed: 00` means
the line check passed, not that the phone is provisioned. The
[MTR2 example](python/examples/install_mtr2.py) runs through this check and stops.

For an answer-detection test, let the firmware finish dialing before clicking
**Answer call**. The button schedules a polarity change and modem answer tone,
followed by carrier. The default delay before the first signal is 3000 simulated
milliseconds. **Reverse polarity** changes the signal immediately; the firmware
may miss the change if it hasn't started listening yet.

For polarity only, send this body to `POST /api/devices`:

```json
{"type":"answer","data":{"method":"polarity","delayMs":3000}}
```

## Card reader

**Magnetic cards.** Enter Track 2 text, click **Insert magnetic card**, and click
**Remove** when the firmware prompts you. The field accepts 1–37 characters
consisting of digits and `=`; the surrounding `;` and `?` are optional. The
reader models movement and feeds samples through the board's DMA channel
(0 on MTR1, 1 on MTR2). The emulator generates parity and a check character;
the firmware decides whether to accept the card.

**Smart cards.** Enter hex or load a 13–32 byte GPM103 image, then click
**Insert smart card**. The first 13 bytes contain the manufacturer area, issuer
area, and debit counters. The emulator protects the manufacturer and issuer
areas. **Write-protect smart card** also blocks counter writes. The firmware
still checks the card type, issuer, and credit.

Use **Export card state** or `GET /api/cards` to inspect the current image
after a debit. A machine snapshot also saves the reader position and any
movement in progress.

## Connect an NCC server

Under **NCC TCP target**, choose **Arm target**, enter the host and port of
your NCC server, and click **Apply**. The emulator doesn't include an NCC server.
The bridge opens a TCP connection on the first ASCI1 transmission with the
modem relay seized, and closes it when the firmware releases the modem. Bytes
pass unchanged; the bridge neither generates responses nor supplies provisioning
data. The NCC server must offer a table set the firmware supports.

- **Simulate modem handshake** supplies answer tone followed by carrier.
  Turn it off to control those signals yourself.
- **Receive latency** models modem turnaround. The default is 100 simulated ms
  so a fast reply doesn't reach the firmware before transmission completes.
  After the remote side closes, the bridge delivers queued bytes and allows
  another 500 simulated ms before dropping carrier if the call is still active.

Check `GET /api/ncc` for connection status and byte counts. For offline replay,
disable the bridge (`POST /api/ncc` with `{"mode":"off"}`), restore a snapshot,
then replay the recorded inputs. The emulator logs inbound bytes as serial
inputs, but snapshots don't preserve the TCP connection or external server state.

## Two-line display

The ROM controls scrolling. Horizontal mode (`0x15`) scrolls the selected row
when a write overflows its right edge. Addressing the last cell directly clears
the overflow latch, so the next write replaces that cell. Vertical mode (`0x12`)
shifts the bottom row up on overflow; normal mode (`0x11`) wraps across both rows.

`GET /api/state` includes the displayed text and `displayHardware`. The browser
uses `/ws` for updates; HTTP clients can use `/api/events/stream?display=1`.
