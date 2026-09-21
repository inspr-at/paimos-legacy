#!/usr/bin/env python3
"""Read-only installed Codex model inventory; never creates a thread or turn.

Protocol: https://developers.openai.com/codex/app-server/#list-models-modellist
Normal CLI auth inheritance is intentional; no HOME/CODEX_HOME overrides.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import time


def inventory(binary, expected_hash, workdir):
    if hashlib.sha256(Path(binary).read_bytes()).hexdigest() != expected_hash:
        raise ValueError("native binary pin mismatch")
    p = subprocess.Popen([binary, "app-server", "--listen", "stdio://"], cwd=workdir,
                         stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                         stderr=subprocess.DEVNULL, start_new_session=True)
    r = {"status": "FAIL", "native_binary_sha256": expected_hash,
         "owned_pgid": p.pid, "query_count": 0, "input_turn_count": 0,
         "methods": [], "models": [], "cleanup_complete": False}
    selector = selectors.DefaultSelector()
    selector.register(p.stdout, selectors.EVENT_READ)
    pending = bytearray()
    deadline = time.monotonic() + 20

    def send(value):
        method = value["method"]
        if method not in ("initialize", "initialized", "model/list"):
            raise ValueError("read-only inventory method required")
        r["methods"].append(method)
        p.stdin.write((json.dumps(value) + "\n").encode()); p.stdin.flush()

    def response(request_id):
        while time.monotonic() < deadline:
            while b"\n" in pending:
                line, _, rest = pending.partition(b"\n"); pending[:] = rest
                frame = json.loads(line)
                if frame.get("id") == request_id:
                    if "error" in frame:
                        r["rpc_error_code"] = frame["error"].get("code")
                        raise ValueError("inventory RPC rejected")
                    return frame["result"]
            if selector.select(min(0.5, max(0, deadline - time.monotonic()))):
                block = os.read(p.stdout.fileno(), 65536)
                if not block: raise ValueError("inventory stream closed")
                pending.extend(block)
                if len(pending) > 8 << 20: raise ValueError("inventory frame bound")
        raise TimeoutError("inventory deadline")

    try:
        if os.getpgid(p.pid) != p.pid: raise ValueError("owned process group mismatch")
        send({"id": 1, "method": "initialize", "params": {"clientInfo": {"name": "pai928-model-inventory", "version": "1"}, "capabilities": {"experimentalApi": True}}})
        response(1)
        send({"method": "initialized", "params": {}})
        cursor = None
        for page in range(4):
            params = {"limit": 100, "includeHidden": True}
            if cursor: params["cursor"] = cursor
            send({"id": page + 2, "method": "model/list", "params": params})
            result = response(page + 2)
            for model in result["data"]:
                # Deliberately omit descriptions, account/config, and payloads.
                r["models"].append({"id": model["id"], "model": model["model"], "hidden": model.get("hidden", False),
                                    "efforts": [e["reasoningEffort"] for e in model.get("supportedReasoningEfforts", [])]})
            cursor = result.get("nextCursor")
            if not cursor: break
        if cursor: raise ValueError("inventory pagination bound")
        r["profile_model_available"] = any(m["model"] == "gpt-6-sol" and "high" in m["efforts"] for m in r["models"])
        r["status"] = "PASS"
    except (ValueError, TimeoutError, OSError, KeyError, json.JSONDecodeError) as exc:
        r["failure_class"] = type(exc).__name__
    finally:
        p.stdin.close()
        try: p.wait(timeout=3)
        except subprocess.TimeoutExpired:
            # Authority is the exact freshly spawned group, never a saved PID.
            os.killpg(p.pid, signal.SIGTERM)
            try: p.wait(timeout=2)
            except subprocess.TimeoutExpired:
                os.killpg(p.pid, signal.SIGKILL); p.wait(timeout=2)
        selector.close(); p.stdout.close()
        end = time.monotonic() + 2
        while time.monotonic() < end:
            try: os.killpg(p.pid, 0)
            except ProcessLookupError:
                r["cleanup_complete"] = True; break
            time.sleep(0.01)
        if not r["cleanup_complete"]: r["status"] = "FAIL"
    return r


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", required=True)
    args = parser.parse_args()
    plan = json.loads(Path(args.plan).read_text())
    root = Path(plan["root"])
    workdir = root / "model-inventory-workspace"
    workdir.mkdir(mode=0o700)
    target = root / "codex-model-inventory.json"
    if target.exists(): raise SystemExit("inventory receipt already exists")
    result = inventory(plan["codex"], plan["codex_sha256"], workdir)
    result["script_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    fd = os.open(target, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as f: json.dump(result, f, indent=2); f.write("\n")
    print(json.dumps({"status": result["status"], "profile_model_available": result.get("profile_model_available"), "cleanup_complete": result["cleanup_complete"], "receipt": str(target)}))
    raise SystemExit(0 if result["status"] == "PASS" else 1)
