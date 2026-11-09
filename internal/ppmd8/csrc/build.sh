#!/bin/sh
# Rebuild ppmd8.wasm.gz from the vendored public-domain 7-Zip PPMd8 sources.
# Requires zig (https://ziglang.org/), tested with 0.17.1.
set -eu

cd "$(dirname "$0")"
ZIG="${ZIG:-zig}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

"$ZIG" cc -target wasm32-freestanding -O2 -nostdlib -fno-builtin \
  -Wl,--no-entry \
  -Wl,--export=ppmd_decode -Wl,--export=malloc -Wl,--export=free \
  -Wl,--export-memory \
  -Wl,--initial-memory=8388608 -Wl,--max-memory=1073741824 \
  -o "$tmp/ppmd8.wasm" ppmd8_shim.c Ppmd8.c Ppmd8Dec.c

gzip -9 -n -c "$tmp/ppmd8.wasm" > ../ppmd8.wasm.gz
echo "wrote ../ppmd8.wasm.gz"
