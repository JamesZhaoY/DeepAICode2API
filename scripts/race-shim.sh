#!/bin/sh
# 让 `go test -race` 在 Windows + scoop mingw 上可用。
#
# 背景：mingw 的 *runtime* 发行包（如 16.1.0-rt_v14-rev1）只带了
# ld.bfd.exe，没有 ld.exe；而 race detector 必须走 cgo 链接，
# collect2 会去找 `ld` 并报 “cannot find 'ld'”。
#
# 解决：在 gcc 同目录补一个 ld.exe（ld.bfd.exe 的副本）。
# GNU binutils 的 ld.bfd 本身就是一个完整链接器，改名后行为一致。
#
# 用法：
#   sh scripts/race-shim.sh          # 创建（已存在则跳过）
#   sh scripts/race-shim.sh --check  # 只检查，缺失时返回非 0
#
# scoop 更新 mingw 后可能再次丢失，重新跑一次即可。
set -eu

case "$(uname -s)" in
MINGW* | MSYS* | CYGWIN*) ;;
*)
	echo "非 Windows 环境，无需处理。"
	exit 0
	;;
esac

cc=$(command -v gcc || true)
if [ -z "$cc" ]; then
	echo "找不到 gcc：请先安装可用的 C 工具链（go test -race 需要 cgo）。" >&2
	exit 1
fi

dir=$(dirname "$cc")

if [ -f "$dir/ld.exe" ]; then
	echo "已就绪：$dir/ld.exe"
	exit 0
fi

if [ "${1:-}" = "--check" ]; then
	echo "缺少 ld.exe（$dir）：go test -race 会链接失败。" >&2
	echo "运行 sh scripts/race-shim.sh 修复。" >&2
	exit 1
fi

if [ ! -f "$dir/ld.bfd.exe" ]; then
	echo "既没有 ld.exe 也没有 ld.bfd.exe（$dir）。" >&2
	echo "请安装完整 binutils，或改用支持 race 的 MSVC / WSL 工具链。" >&2
	exit 1
fi

cp "$dir/ld.bfd.exe" "$dir/ld.exe"
echo "已创建 $dir/ld.exe（ld.bfd.exe 的副本）"
"$dir/ld.exe" --version | head -1
