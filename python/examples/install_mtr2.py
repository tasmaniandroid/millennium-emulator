"""Run the MTR2 craft menu and C.O. line test on the NLA1X06 flash image.

Run from the project root: python python/examples/install_mtr2.py PATH_TO_FLASH.BIN
"""

import argparse
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

from millennium import Phone  # noqa: E402

def phase(phone, name):
    print(f"[{name}] {phone.display}")


def expect(phone, pattern, name):
    text = "".join(phone.display)
    if not re.search(pattern, text, re.I):
        raise SystemExit(f"{name}: display {text!r} does not match /{pattern}/")


parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
parser.add_argument("rom", type=Path, help="path to the NLA1X06 flash.bin image")
rom = parser.parse_args().rom.resolve()
if not rom.is_file():
    parser.error(f"ROM file not found: {rom}")

with Phone() as phone:
    info = phone.load(str(rom), board="mtr2")
    print("loaded:", info["firmware"]["path"], "board:", info["board"]["id"],
          "clock:", info["board"]["clockHz"])

    # Boot: ~10 simulated seconds for full background init.
    phone.run_ms(10_000)
    expect(phone, r"out of service", "BOOT")
    phase(phone, "BOOT")

    # Craft entry: cover open, then 55555* at ~400ms per key.
    phone.sensor(cover_open=True, run_instructions=0)
    phone.run_ms(2_000)
    for k in "55555*":
        phone.device("key", {"key": k})
        phone.run_ms(400)
    expect(phone, r"NLA1X06", "CRAFT PIN")
    phase(phone, "CRAFT PIN")

    # '#' -> installation status, '#' again -> installation / C.O. test.
    phone.device("key", {"key": "#"})
    phone.run_ms(2_000)
    expect(phone, r"Not installed", "INSTALL STATUS")
    phase(phone, "INSTALL STATUS")

    phone.device("key", {"key": "#"})
    phone.run_ms(2_500)
    expect(phone, r"C\.O\.", "INSTALL")
    phase(phone, "INSTALL")

    # C.O. line test: lift the handset; success shows Completed: 00.
    phone.handset(True, run_instructions=0)
    for _ in range(30):
        if "Completed:" in "".join(phone.display):
            break
        phone.run_ms(500)
    expect(phone, r"Completed:\s+00", "C.O. TEST")
    phase(phone, "C.O. TEST")

    # Full provisioning continues from here with an NCC target:
    #   phone.ncc_configure(host="127.0.0.1", port=16420)
    print("SUCCESS: craft PIN accepted and C.O. test completed with 00")
