#!/usr/bin/env python3
"""Run the bridge only while the Beeper desktop app is running on this Mac."""
import os
from pathlib import Path
import signal
import subprocess
import time

ROOT = Path(__file__).resolve().parent.parent
os.umask(0o077)
child = None
running = True

def stop(*_):
    global running
    running = False

signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)

def stop_child():
    global child
    if child is not None and child.poll() is None:
        child.terminate()
        try:
            child.wait(timeout=15)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()
    child = None

try:
    while running:
        opened = subprocess.run(
            ["pgrep", "-f", r"/Beeper( Desktop)?\.app/Contents/MacOS/Beeper( Desktop)?$"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        ).returncode == 0
        if opened and child is None:
            print("Beeper is open; starting Work Teams bridge.", flush=True)
            child = subprocess.Popen([str(ROOT / "scripts/run.sh")], cwd=ROOT)
        elif not opened and child is not None:
            print("Beeper closed; stopping Work Teams bridge.", flush=True)
            stop_child()
        elif child is not None and child.poll() is not None:
            raise SystemExit(f"Bridge exited with code {child.returncode}. Check its log before restarting.")
        time.sleep(2)
finally:
    stop_child()
