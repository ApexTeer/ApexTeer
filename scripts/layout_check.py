#!/usr/bin/env python3
"""Check every rendered screen: exact height, no line wider than the terminal.

The renderer writes one frame per screen and is the only way to inspect the layout
without a terminal, so the invariants the panel promises (a frame is exactly as tall
as the window and never wider) are asserted here for every page at every size CI
would try.
"""
import re
import subprocess
import sys

ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")

SCREENS = [
    "", "node", "domain", "subscribe", "service", "bbr",
    "bbr-qdisc", "bbr-versions", "script-update", "uninstall", "system", "form",
    "task", "toolbox-report", "node-protocols", "params", "ports", "sni",
]
SIZES = [(80, 24), (100, 30), (100, 33), (120, 40), (160, 50), (60, 20)]
BIN = sys.argv[1] if len(sys.argv) > 1 else "./easysb.exe"

failures = 0
for width, height in SIZES:
    for screen in SCREENS:
        cmd = [BIN, "--render", "--width", str(width), "--height", str(height),
               "--theme", "dark", "--icons", "symbols", "--language", "C"]
        if screen:
            cmd += ["--screen", screen]
        out = subprocess.run(cmd, capture_output=True).stdout.decode("utf-8", "replace")
        lines = out.split("\n")
        if lines and lines[-1] == "":
            lines.pop()
        label = screen or "root"
        if len(lines) != height:
            print(f"FAIL {label} {width}x{height}: {len(lines)} lines, want {height}")
            failures += 1
        for i, line in enumerate(lines):
            w = len(ANSI.sub("", line))
            if w > width:
                print(f"FAIL {label} {width}x{height}: line {i} is {w} wide")
                failures += 1
                break

print("layout check:", "FAILED" if failures else "ok", f"({failures} problems)")
sys.exit(1 if failures else 0)
