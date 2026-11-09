/* Minimal freestanding build environment for the 7-Zip PPMd8 decoder.
 *
 * The codec itself (Ppmd8.c, Ppmd8Dec.c, Ppmd8.h, Ppmd.h) is public domain
 * code by Igor Pavlov, based on PPMd var.I by Dmitry Shkarin. This header
 * supplies only the handful of 7-Zip platform types/macros those files use,
 * so they can be compiled to a small wasm32 module without libc or CGO.
 */
#ifndef POLKA_PPMD_PRECOMP_H
#define POLKA_PPMD_PRECOMP_H

/* wasm32 has no libc headers in the freestanding target; use compiler
 * predefined scalar types instead. */
typedef __SIZE_TYPE__ size_t;
typedef __UINTPTR_TYPE__ uintptr_t;
typedef unsigned char uint8_t;
typedef unsigned short uint16_t;
typedef unsigned int uint32_t;
typedef int int32_t;

#ifndef NULL
#define NULL ((void *)0)
#endif

/* wasm32: 32-bit pointers, little-endian, no unaligned-access tricks. */
#define MY_CPU_SIZEOF_POINTER 4
#define MY_CPU_LE
#define MY_CPU_pragma_pack_push_1
#define MY_CPU_pragma_pop

#define EXTERN_C_BEGIN
#define EXTERN_C_END

#define MY_ALIGN(n) __attribute__((aligned(n)))
#define Z7_NO_INLINE __attribute__((noinline))
#define Z7_FORCE_INLINE inline __attribute__((always_inline))

typedef uint8_t Byte;
typedef uint16_t UInt16;
typedef uint32_t UInt32;
typedef int32_t Int32;
typedef int BoolInt;
#define True 1
#define False 0

/* PPMd keeps its whole model in one flat allocation. The shim implements a
 * bump allocator over the module's linear memory; every decode gets a fresh
 * wasm instance, so free() is intentionally a no-op. */
void *ppmd_alloc(size_t size);
void ppmd_free(void *ptr);

typedef void *ISzAllocPtr;
#define ISzAlloc_Alloc(alloc, size) ppmd_alloc(size)
#define ISzAlloc_Free(alloc, addr) ppmd_free(addr)

/* Subset of 7-Zip's IByteIn interface used by the range decoder. */
typedef struct IByteIn IByteIn;
struct IByteIn {
  Byte (*Read)(IByteIn *p);
};
typedef IByteIn *IByteInPtr;
typedef void *IByteOutPtr;
#define IByteIn_Read(p) ((p)->Read(p))

/* Freestanding helpers referenced by the codec. */
void *memcpy(void *dst, const void *src, size_t n);
void *memmove(void *dst, const void *src, size_t n);
void *memset(void *dst, int c, size_t n);
int memcmp(const void *a, const void *b, size_t n);

#endif
