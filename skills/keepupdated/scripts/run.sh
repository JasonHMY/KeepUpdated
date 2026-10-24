#!/bin/bash
set -euo pipefail

fail() {
  printf '%s\n' "keepupdated skill: $*" >&2
  exit 2
}

case "${1:-}" in
  scan|check|plan|apply|results|version|--version|help|--help|-h) ;;
  *) fail 'Usage: bash scripts/run.sh scan|check|plan|apply|results|version|--help.' ;;
esac

[ "$(/usr/bin/uname -s)" = Darwin ] || fail '需要 macOS 13+ 的 Apple Silicon Mac。'
[ "$(/usr/bin/uname -m)" = arm64 ] || fail '此发布包只支持 Apple Silicon。'

if [ "${KEEPUPDATED_BIN+x}" = x ]; then
  case "$KEEPUPDATED_BIN" in
    /*) binary=$KEEPUPDATED_BIN ;;
    *) fail 'KEEPUPDATED_BIN 必须是用户显式指定的二进制绝对路径。' ;;
  esac
else
  script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
  binary="$script_dir/../../../keepupdated"
fi

if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
  fail '未找到可执行的配套二进制。请从 https://github.com/JasonHMY/KeepUpdated/releases/tag/v0.1.2 获取 keepupdated_0.1.2_darwin_arm64.tar.gz 和 SHA256SUMS，使用 shasum -a 256 -c SHA256SUMS 校验，再解压完整包。若该版本尚未发布，请等待发布，不执行 main 或其他版本。'
fi

metadata=$("$binary" version --format json) || fail '配套二进制无法返回版本 JSON，已停止。'
field() {
  printf '%s' "$metadata" | /usr/bin/plutil -extract "$1" raw -o - -- - 2>/dev/null
}
version=$(field version) || fail '版本 JSON 缺少 version，已停止。'
os=$(field os) || fail '版本 JSON 缺少 os，已停止。'
arch=$(field arch) || fail '版本 JSON 缺少 arch，已停止。'
schema=$(field schema) || fail '版本 JSON 缺少 schema，已停止。'
[ "$version" = 0.1.2 ] || fail '只接受固定版本 0.1.2；请获取匹配的发布包。'
[ "$os/$arch" = darwin/arm64 ] || fail '二进制平台不是 darwin/arm64，已停止。'
[ "$schema" = keepupdated/v1 ] || fail '二进制 JSON schema 不匹配，已停止。'
plan_schema=$(field plan_schema) || fail '缺少更新预览协议；旧同版只读扫描器不能作为本产品配套程序。'
apply_schema=$(field apply_schema) || fail '缺少确认执行协议；请使用完整产品发布包。'
[ "$plan_schema" = keepupdated/plan/v1 ] || fail '更新预览协议不匹配，已停止。'
[ "$apply_schema" = keepupdated/apply/v1 ] || fail '确认执行协议不匹配，已停止。'

# Older same-version candidates support plan/apply but not persisted results.
if [ "${1:-}" = results ]; then
  recent_schema=$(field recent_operation_schema) || fail 'The paired binary does not declare saved-results support; use the matching complete package.'
  [ "$recent_schema" = keepupdated/recent-operation/v1 ] || fail 'Saved-results schema does not match; execution stopped.'
fi

exec "$binary" "$@"
