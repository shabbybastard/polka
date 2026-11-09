# PPMd8 codec sources

These files implement the decoder for ZIP compression method 98 (PPMd
variant I). Go's `archive/zip` supports only Store and Deflate; optimized
Flibusta distributions store books as PPMd-compressed ZIP entries, so Polka
embeds the reference 7-Zip decoder as a small wasm module and runs it with
wazero (pure Go, no CGO).

## Provenance

- `Ppmd8.c`, `Ppmd8Dec.c`, `Ppmd8.h`, `Ppmd.h`
  — 7-Zip tag `25.01`, files under `C/`.
- Author: Igor Pavlov, based on PPMd var.I (2002) by Dmitry Shkarin and the
  carryless range coder (1999) by Dmitry Subbotin.
- License: **public domain**.

The upstream sources are unchanged except for two mechanical build tweaks:

- `Ppmd8.c` no longer includes `<string.h>` (there is no libc in the
  freestanding wasm target; the helpers are declared in `Precomp.h` and
  implemented in `ppmd8_shim.c`).
- `Ppmd.h` no longer includes `CpuArch.h`; `Precomp.h` provides the few
  platform macros it needs for wasm32.

`Precomp.h` and `ppmd8_shim.c` are Polka additions.

## ZIP framing

A method-98 entry payload starts with two little-endian property bytes:

```
order   = (props & 0x0F) + 1             // 2..16
memory  = 1 MiB * (((props >> 4) & 0xFF) + 1)  // 1..256 MiB
restore = (props >> 12) & 0x0F           // 0 = restart, 1 = cut-off
```

The rest is a PPMd8 range-coded stream. ZIP PPMd has no end marker; the
uncompressed size from the ZIP directory bounds decoding. The shim exposes
`ppmd_decode(in, inLen, out, outLen, order, memMiB, restore)` and a bump
allocator (`malloc`/`free`) over the module's linear memory. Every decode
gets a fresh wasm instance, so `free` is intentionally a no-op.

## Rebuilding

Only needed when changing the codec. Normal `make build` uses the committed
`ppmd8.wasm.gz`. Requires [zig](https://ziglang.org/) (tested with 0.17.1):

```sh
make ppmd-wasm            # or: ZIG=/path/to/zig ./internal/ppmd8/csrc/build.sh
```

The build reserves 1 MiB below the top of linear memory for the C stack;
the bump heap never grows into it.
