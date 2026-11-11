#!/bin/sh
# 构建全部架构的静态二进制。
#
# 用法: ./scripts/build-all.sh [版本号]
#
# 输出到 dist/，含 SHA256SUMS。所有产物均为 CGO_ENABLED=0 的纯静态 ELF，
# 不带任何动态库依赖 —— 一个文件扔到 OpenWrt / CentOS 7 / Ubuntu 都能跑。
set -eu

VERSION="${1:-0.0.0-dev}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w \
  -X slx/internal/buildinfo.Version=${VERSION} \
  -X slx/internal/buildinfo.Commit=${COMMIT} \
  -X slx/internal/buildinfo.BuildTime=${BUILD_TIME}"

OUT=dist
rm -rf "$OUT"
mkdir -p "$OUT"

# 目标架构。mipsel 是 MT7621 等 OpenWrt 主力芯片，必须支持。
TARGETS="amd64 arm64 arm mipsle riscv64"

export CGO_ENABLED=0
export GOOS=linux

for arch in $TARGETS; do
  case "$arch" in
    arm)     export GOARCH=arm;   export GOARM=7 ;;
    mipsle)  export GOARCH=mipsle; export GOMIPS=softfloat ;;
    *)       export GOARCH=$arch;  unset GOARM 2>/dev/null || true; unset GOMIPS 2>/dev/null || true ;;
  esac

  out="$OUT/slx-linux-$arch"
  printf '  %-12s ' "linux/$arch"
  if go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/slx 2>/dev/null; then
    printf 'OK   %s KB\n' "$(( $(wc -c < "$out") / 1024 ))"
  else
    printf 'FAIL\n'
    exit 1
  fi
done

# 校验和，便于分发后验证完整性
( cd "$OUT" && sha256sum slx-linux-* > SHA256SUMS )

echo ""
echo "产物已生成于 $OUT/ ："
ls -l "$OUT"
echo ""
echo "校验和："
cat "$OUT/SHA256SUMS"
