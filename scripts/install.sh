#!/usr/bin/env bash
set -euo pipefail
project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -x "$project_root/wenshu-mcp" ]]; then
  exec "$project_root/wenshu-mcp" setup "$@"
fi
exec "$project_root/bin/wenshu-mcp" setup "$@"
