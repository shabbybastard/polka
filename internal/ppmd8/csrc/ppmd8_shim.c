/* Freestanding wasm shim for the 7-Zip PPMd8 decoder.
 *
 * Exported to Go:
 *   malloc(size)                      -- bump allocator over linear memory
 *   free(ptr)                         -- no-op (one decode per instance)
 *   ppmd_decode(in, inLen, out, outLen, order, memMiB, restore) -> status
 *
 * status: 0 ok, 1 model allocation failed, 2 range-decoder init failed,
 *         3 corrupt/truncated stream, 4 input overrun, 5 bad parameters.
 */
#include "Precomp.h"
#include "Ppmd8.h"

extern unsigned char __heap_base;

/* wasm-ld puts the C stack at the top of linear memory, growing downward.
 * Keep a generous reserve so the bump heap can never overwrite it. */
#define STACK_RESERVE (1u << 20)

static uintptr_t heap_ptr;
static uintptr_t heap_end;

static void heap_refresh(void) {
  uintptr_t top = (uintptr_t)__builtin_wasm_memory_size(0) * 65536u;
  heap_end = top > STACK_RESERVE ? top - STACK_RESERVE : 0;
}

static void heap_init(void) {
  if (heap_ptr == 0) {
    heap_ptr = ((uintptr_t)&__heap_base + 15u) & ~(uintptr_t)15u;
    heap_refresh();
  }
}

__attribute__((visibility("default")))
void *malloc(size_t size) {
  heap_init();
  size = (size + 15u) & ~(size_t)15u;
  if (heap_ptr + size > heap_end) {
    size_t need = (heap_ptr + size + STACK_RESERVE) -
                  (uintptr_t)__builtin_wasm_memory_size(0) * 65536u;
    size_t pages = (need + 65535u) / 65536u;
    if (__builtin_wasm_memory_grow(0, pages) == (size_t)-1) {
      return 0;
    }
    heap_refresh();
    if (heap_ptr + size > heap_end) {
      return 0;
    }
  }
  void *p = (void *)heap_ptr;
  heap_ptr += size;
  return p;
}

__attribute__((visibility("default")))
void free(void *ptr) {
  (void)ptr;
}

void *ppmd_alloc(size_t size) { return malloc(size); }
void ppmd_free(void *ptr) { (void)ptr; }

void *memcpy(void *dst, const void *src, size_t n) {
  Byte *d = (Byte *)dst;
  const Byte *s = (const Byte *)src;
  for (size_t i = 0; i < n; i++) d[i] = s[i];
  return dst;
}

void *memmove(void *dst, const void *src, size_t n) {
  Byte *d = (Byte *)dst;
  const Byte *s = (const Byte *)src;
  if (d == s || n == 0) return dst;
  if (d < s) {
    for (size_t i = 0; i < n; i++) d[i] = s[i];
  } else {
    for (size_t i = n; i > 0; i--) d[i - 1] = s[i - 1];
  }
  return dst;
}

void *memset(void *dst, int c, size_t n) {
  Byte *d = (Byte *)dst;
  for (size_t i = 0; i < n; i++) d[i] = (Byte)c;
  return dst;
}

int memcmp(const void *a, const void *b, size_t n) {
  const Byte *x = (const Byte *)a;
  const Byte *y = (const Byte *)b;
  for (size_t i = 0; i < n; i++) {
    if (x[i] != y[i]) return (int)x[i] - (int)y[i];
  }
  return 0;
}

typedef struct {
  IByteIn vt;
  const Byte *cur;
  const Byte *end;
  UInt32 overrun;
} MemIn;

static Byte mem_read(IByteIn *p) {
  MemIn *m = (MemIn *)p;
  if (m->cur >= m->end) {
    m->overrun++;
    return 0;
  }
  return *m->cur++;
}

__attribute__((visibility("default")))
int ppmd_decode(const unsigned char *in, unsigned in_len,
                unsigned char *out, unsigned out_len,
                unsigned order, unsigned mem_mb, unsigned restore) {
  if (order < 2 || order > 16 || mem_mb < 1 || mem_mb > 256 || restore > 1) {
    return 5;
  }

  CPpmd8 p;
  Ppmd8_Construct(&p);
  if (!Ppmd8_Alloc(&p, (UInt32)mem_mb << 20, 0)) {
    return 1;
  }

  MemIn mi;
  mi.vt.Read = mem_read;
  mi.cur = in;
  mi.end = in + in_len;
  mi.overrun = 0;

  Ppmd8_Init(&p, order, restore);
  p.Stream.In = &mi.vt;
  if (!Ppmd8_Init_RangeDec(&p)) {
    Ppmd8_Free(&p, 0);
    return 2;
  }

  for (unsigned i = 0; i < out_len; i++) {
    int sym = Ppmd8_DecodeSymbol(&p);
    if (sym < 0) {
      Ppmd8_Free(&p, 0);
      return 3;
    }
    out[i] = (Byte)sym;
  }

  Ppmd8_Free(&p, 0);
  return mi.overrun ? 4 : 0;
}
