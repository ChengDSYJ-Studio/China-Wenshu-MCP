"""Build complete native release archives. Requires Go on the build machine."""
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import os
import re
import shutil
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parent.parent
GO = os.environ.get("GO", "go")
VERSION = re.search(r'const Version = "([^"]+)"', (ROOT / "internal/wenshu/model.go").read_text()).group(1)
TARGETS = [("darwin", "arm64", "macOS-arm64"), ("darwin", "amd64", "macOS-x64"),
           ("windows", "amd64", "Windows-x64"), ("windows", "arm64", "Windows-arm64"),
           ("linux", "amd64", "Linux-x64"), ("linux", "arm64", "Linux-arm64")]

def build(target):
    system, arch, label = target
    folder = ROOT / "dist" / f"wenshu-mcp-{VERSION}-{label}"
    folder.mkdir(parents=True, exist_ok=True)
    exe = "wenshu-mcp.exe" if system == "windows" else "wenshu-mcp"
    env = {**os.environ, "GOOS": system, "GOARCH": arch, "CGO_ENABLED": "0"}
    env.setdefault("GOPROXY", "https://goproxy.cn,direct")
    print(f"Building {label}", flush=True)
    subprocess.run([GO, "build", "-trimpath", "-ldflags=-s -w", "-o", str(folder / exe), "./cmd/wenshu-mcp"], cwd=ROOT, env=env, check=True)
    shutil.copy2(ROOT / "README.md", folder / "README.md")
    for name in ("docs", "examples", "cmd", "internal", "scripts"):
        shutil.copytree(ROOT / name, folder / name, dirs_exist_ok=True)
    for name in ("go.mod", "go.sum"):
        shutil.copy2(ROOT / name, folder / name)
    if system == "windows":
        launcher = '@echo off\r\nchcp 65001 >nul\r\ncd /d "%~dp0"\r\nwenshu-mcp.exe setup %*\r\necho.\r\npause\r\n'
        (folder / "安装设置.cmd").write_bytes(launcher.encode("utf-8"))
    else:
        name = "安装设置.command" if system == "darwin" else "安装设置.sh"
        launcher = '#!/usr/bin/env bash\nset -u\ncd "$(dirname "$0")"\nchmod +x ./wenshu-mcp\n./wenshu-mcp setup "$@"\nresult=$?\nprintf "\\n按回车关闭窗口…"\nread -r reply || true\nexit "$result"\n'
        (folder / name).write_text(launcher, encoding="utf-8")
        (folder / name).chmod(0o755)
        (folder / exe).chmod(0o755)
    intro = f"裁判文书网 MCP {VERSION}\n\n打开“安装设置”，选择客户端即可完成接入。\n浏览器下载默认使用国内镜像，也可以选择官方源。\n安装后对 AI 说：帮我登录裁判文书网。\n"
    (folder / "开始使用.txt").write_text(intro, encoding="utf-8-sig")
    archive = folder.parent / (folder.name + ".zip")
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for p in sorted(folder.rglob("*")):
            if p.is_file():
                z.write(p, Path(folder.name) / p.relative_to(folder))
    print(f"Ready {label}: {archive.name}", flush=True)
    return archive

if __name__ == "__main__":
    with ThreadPoolExecutor(max_workers=3) as pool:
        archives = list(pool.map(build, TARGETS))
    print("\n".join(str(x) for x in archives), flush=True)
