"""Exercise the built stdio server with JSON-RPC, using only Python stdlib."""
import json
import select
import subprocess
import sys
import tempfile
from pathlib import Path

binary = str(Path(sys.argv[1] if len(sys.argv) > 1 else "bin/wenshu-mcp").resolve())
with tempfile.TemporaryDirectory(prefix="wenshu-mcp-smoke-") as data:
    p = subprocess.Popen([binary, "--data-dir", data, "serve"], stdin=subprocess.PIPE,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1)
    sequence = 0
    def call(method, params):
        global sequence
        sequence += 1
        p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": sequence, "method": method, "params": params}) + "\n")
        p.stdin.flush()
        while True:
            if not select.select([p.stdout], [], [], 20)[0]:
                raise TimeoutError(method)
            line = p.stdout.readline()
            if not line:
                raise RuntimeError(p.stderr.read())
            value = json.loads(line)
            if value.get("id") == sequence:
                assert "error" not in value, value
                assert not value["result"].get("isError"), value
                return value["result"]
    try:
        call("initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                            "clientInfo": {"name": "stdio-smoke", "version": "1"}})
        p.stdin.write('{"jsonrpc":"2.0","method":"notifications/initialized"}\n')
        p.stdin.flush()
        tools = call("tools/list", {})["tools"]
        assert len(tools) == 22
        call("tools/call", {"name": "wenshu_session_status", "arguments": {}})
        call("tools/call", {"name": "wenshu_task_create", "arguments": {
            "name": "stdio test", "query": {"keyword": "测试"}, "fetch_body": False}})
        call("tools/call", {"name": "wenshu_task_list", "arguments": {}})
        call("tools/call", {"name": "wenshu_export", "arguments": {
            "directory": str(Path(data) / "export")}})
        assert (Path(data) / "export" / "documents.dta").exists()
        print("stdio initialize, 22 tools, session/task/export calls: PASS")
    finally:
        p.stdin.close()
        try:
            p.wait(timeout=10)
        except subprocess.TimeoutExpired:
            p.terminate()
            p.wait(timeout=10)
