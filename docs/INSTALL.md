# 安装设置

## 三步开始

1. 下载对应系统与架构的 1.0 发行包并解压。
2. macOS 双击 `安装设置.command`，Windows 双击 `安装设置.cmd`；Linux 运行 `./安装设置.sh`。
3. 在向导中选择客户端，完成后重载客户端的 MCP 服务，对 AI 说“帮我登录裁判文书网”。

## 向导完成的工作

- 把程序安装到数据目录的 `bin/`。
- 自动发现 Chrome、Edge、Chromium、Brave，保存浏览器路径。
- 按需下载配套 Chrome for Testing。
- 合并客户端配置，保留既有服务并备份原配置。
- 通过实际 MCP 握手读取工具清单，显示服务已就绪。

支持自动配置 Codex、Claude Desktop、Claude Code、Cursor、Windsurf、Gemini CLI。选择“通用 MCP 配置”会生成可导入的 JSON 文件。

## 国内镜像

配套浏览器默认下载顺序：

1. `https://registry.npmmirror.com/-/binary/chrome-for-testing/`
2. `https://cdn.npmmirror.com/binaries/chrome-for-testing/`
3. `https://storage.googleapis.com/chrome-for-testing-public/`

向导提供“国内镜像”和“官方源”选项。也可以指定自定义镜像根地址，文件路径会追加 `版本/平台/chrome-平台.zip`。

```sh
./wenshu-mcp setup --client codex --download-source cn
./wenshu-mcp setup --client codex --download-source official
./wenshu-mcp setup --client codex --download-source https://mirror.example/chrome-for-testing
```

`WENSHU_DOWNLOAD_SOURCE` 可设置默认下载源。浏览器路径与下载源写入数据目录的 `config.json`。

## 批量或自定义安装

```sh
./wenshu-mcp setup --client codex,claude-desktop
./wenshu-mcp setup --client codex --browser "/完整路径/浏览器"
./wenshu-mcp --data-dir "/完整路径/数据目录" setup --client codex
```

可使用 `--config-path` 指定单个客户端的配置文件。

## 安装位置

默认数据目录为 `~/.wenshu-mcp`。安装后，客户端配置指向数据目录中的程序；解压目录可以移动或归档。

| 内容 | 位置 |
| --- | --- |
| 程序 | `bin/` |
| 浏览器和下载源设置 | `config.json` |
| 配套浏览器 | `runtime/` |
| 会话 | `session.json` |
| 专用浏览器配置 | `browser-profile/` |
| 语料、任务和编码 | `corpus.sqlite` |

## 使用已有会话

让 AI 调用 `wenshu_auth_import`，传入本地会话文件路径。随后调用 `wenshu_session_status` 查看状态。

## 更新

解压新发行包，再次运行安装设置。向导更新程序和客户端连接；语料、任务、登录配置继续保存在原数据目录中。重载 MCP 服务后启用更新。

## 源码构建

```sh
export GOPROXY=https://goproxy.cn,direct
go build -trimpath -o wenshu-mcp ./cmd/wenshu-mcp
```

完整发行包构建脚本为 `scripts/build-release.py`。
