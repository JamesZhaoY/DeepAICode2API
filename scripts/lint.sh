#!/bin/sh
# 与 CI 一致的本地检查：格式 + vet + 测试。
set -eu

cd "$(dirname "$0")/.."

files=$(gofmt -l . || true)
if [ -n "$files" ]; then
	echo "以下文件未格式化："
	echo "$files"
	echo "运行 make fmt 修复。"
	exit 1
fi

go vet ./...
go test -count=1 ./...
