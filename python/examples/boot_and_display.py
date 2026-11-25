"""Minimal example: boot TEST.BIN and interact with the display.

Run from the project root: python python/examples/boot_and_display.py PATH_TO_TEST.BIN
"""

import argparse
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

from millennium import Phone  # noqa: E402

parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
parser.add_argument("rom", type=Path, help="path to the TEST.BIN image")
rom = parser.parse_args().rom.resolve()
if not rom.is_file():
    parser.error(f"ROM file not found: {rom}")

with Phone() as phone:
    phone.load(str(rom), board="mtr1")
    phone.run(2_000_000)

    # Deterministic wait: bounded-run until the boot screen appears.
    lines = phone.wait_for_display("out of service", max_instructions=10_000_000)
    print("boot display:", lines)

    # Press a few keys and show what the firmware does with them.
    last_event = phone.events()[-1]["id"]
    phone.keys("123", per_key=300_000)
    for event in phone.events(since=last_event):
        if event["type"] in ("input.key", "dial"):
            print(event["type"], {k: v for k, v in event.items()
                                  if k not in ("id", "cycle", "type")})

    # Snapshot, perturb, restore — scripts can checkpoint freely.
    phone.snapshot_save("checkpoint")
    phone.run(1_000_000)
    phone.snapshot_restore("checkpoint")
    print("restored at cycles:", phone.state["cpu"]["cycles"])

    print("final display:", phone.display)
