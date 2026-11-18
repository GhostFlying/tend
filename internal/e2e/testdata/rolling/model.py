#!/usr/bin/env python3
"""Local app-server fixture. Only the model is substituted at the process boundary."""
import json
import os
import sys
import threading
import uuid
from pathlib import Path

root = Path(os.environ["TEND_COMPAT_BARRIER"])
thread = str(uuid.uuid4())
turn = str(uuid.uuid4())
write_lock = threading.Lock()


def send(message):
    with write_lock:
        print(json.dumps(message), flush=True)


def submit_after_cutover():
    gate_path = root / (thread + ".gate")
    os.mkfifo(gate_path, 0o600)
    # A FIFO ties completion to the tested transition, independent of machine speed.
    with gate_path.open() as gate:
        gate.readline()
    send({"id": "submit", "method": "item/tool/call", "params": {
        "threadId": thread, "turnId": turn, "callId": "submit", "tool": "submit_result",
        "arguments": {"report": {"body": "compatibility report"}},
    }})


for line in sys.stdin:
    request = json.loads(line)
    if "id" not in request:
        continue
    method = request.get("method")
    if method is None:
        send({"method": "turn/completed", "params": {"threadId": thread, "turn": {"id": turn, "status": "completed"}}})
        continue
    result = {}
    if method in ("thread/start", "thread/resume"):
        result = {"thread": {"id": thread}}
    if method == "turn/start":
        result = {"turn": {"id": turn}}
    send({"id": request["id"], "result": result})
    if method == "turn/start":
        threading.Thread(target=submit_after_cutover, daemon=True).start()
    if method == "turn/interrupt":
        (root / (thread + ".interrupted")).touch()
        send({"method": "turn/completed", "params": {"threadId": thread, "turn": {"id": turn, "status": "interrupted"}}})
