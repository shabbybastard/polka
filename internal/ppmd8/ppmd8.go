// Package ppmd8 decodes ZIP entries compressed with PPMd variant I
// (ZIP compression method 98).
//
// Go's archive/zip only supports Store and Deflate. PPMd is an exotic but
// real method used by optimized Flibusta distributions; there is no pure-Go
// decoder for it. Instead of porting the ~1900-line model by hand, this
// package embeds the public-domain Ppmd8 decoder from 7-Zip, compiled to a
// small WebAssembly module, and runs it with wazero (pure Go, no CGO), the
// same approach the project already uses for JPEG XL covers.
package ppmd8

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// MaxOutputSize is the largest uncompressed PPMd entry accepted. A single
// decode holds the compressed input, the output and the model in wasm
// memory; this bound keeps a malicious archive from exhausting the server.
const MaxOutputSize = 64 << 20

// maxConcurrentDecodes bounds how many PPMd models may be alive at once.
// A model can use up to 256 MiB (the ZIP property encodes 1..256 MiB).
const maxConcurrentDecodes = 2

const (
	// Runtime memory limit: 512 MiB per instance. Enough for a 256 MiB
	// model + 64 MiB output + compressed input.
	runtimeMemoryPages = (512 << 20) / 65536

	// Status codes returned by ppmd_decode.
	statusOK          = 0
	statusAllocFailed = 1
	statusRangeInit   = 2
	statusCorrupt     = 3
	statusOverrun     = 4
	statusBadParams   = 5
)

var (
	//go:embed ppmd8.wasm.gz
	wasmGz []byte

	initOnce sync.Once
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	initErr  error

	decodeSem   = make(chan struct{}, maxConcurrentDecodes)
	instanceSeq atomic.Uint64
)

// ErrCorrupt reports a malformed or truncated PPMd stream.
var ErrCorrupt = errors.New("ppmd8: corrupt or truncated stream")

// ErrUnsupported reports properties the decoder cannot handle.
var ErrUnsupported = errors.New("ppmd8: unsupported PPMd properties")

// ErrTooLarge reports an entry larger than MaxOutputSize.
var ErrTooLarge = fmt.Errorf("ppmd8: entry exceeds %d bytes", MaxOutputSize)

func initRuntime() {
	zr, err := gzip.NewReader(bytes.NewReader(wasmGz))
	if err != nil {
		initErr = fmt.Errorf("ppmd8: unpack wasm: %w", err)
		return
	}
	defer zr.Close()
	wasm, err := io.ReadAll(zr)
	if err != nil {
		initErr = fmt.Errorf("ppmd8: unpack wasm: %w", err)
		return
	}
	runtime = wazero.NewRuntimeWithConfig(context.Background(),
		wazero.NewRuntimeConfig().WithMemoryLimitPages(runtimeMemoryPages))
	compiled, initErr = runtime.CompileModule(context.Background(), wasm)
	if initErr != nil {
		initErr = fmt.Errorf("ppmd8: compile wasm: %w", initErr)
	}
}

// Decode decompresses one ZIP entry. data is the raw entry payload as
// returned by zip.File.OpenRaw: two property bytes followed by the PPMd8
// range-coded stream. size is the uncompressed size declared in the ZIP
// directory; PPMd8 streams in ZIP have no end marker, so the size is what
// stops decoding.
func Decode(data []byte, size int64) ([]byte, error) {
	if size < 0 || size > MaxOutputSize {
		return nil, ErrTooLarge
	}
	if len(data) < 2 {
		return nil, ErrCorrupt
	}
	if size == 0 {
		return []byte{}, nil
	}

	props := binary.LittleEndian.Uint16(data[:2])
	order := int(props&0x0f) + 1
	memMiB := int((props>>4)&0xff) + 1
	restore := int((props >> 12) & 0x0f)
	if order < 2 || order > 16 || restore > 1 {
		return nil, fmt.Errorf("%w: order=%d restore=%d", ErrUnsupported, order, restore)
	}

	initOnce.Do(initRuntime)
	if initErr != nil {
		return nil, initErr
	}

	decodeSem <- struct{}{}
	defer func() { <-decodeSem }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Each instance needs a unique name in the wazero namespace; concurrent
	// decodes would otherwise collide.
	name := fmt.Sprintf("ppmd8-%d", instanceSeq.Add(1))
	inst, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(name))
	if err != nil {
		return nil, fmt.Errorf("ppmd8: instantiate wasm: %w", err)
	}
	defer inst.Close(ctx)

	mallocFn := inst.ExportedFunction("malloc")
	if mallocFn == nil {
		return nil, errors.New("ppmd8: wasm module has no malloc")
	}
	decodeFn := inst.ExportedFunction("ppmd_decode")
	if decodeFn == nil {
		return nil, errors.New("ppmd8: wasm module has no ppmd_decode")
	}

	in := data[2:]
	inPtr, err := wasmAlloc(ctx, mallocFn, len(in))
	if err != nil {
		return nil, err
	}
	outPtr, err := wasmAlloc(ctx, mallocFn, int(size))
	if err != nil {
		return nil, err
	}
	if !inst.Memory().Write(inPtr, in) {
		return nil, errors.New("ppmd8: write input to wasm memory")
	}

	res, err := decodeFn.Call(ctx,
		uint64(inPtr), uint64(len(in)),
		uint64(outPtr), uint64(size),
		uint64(order), uint64(memMiB), uint64(restore))
	if err != nil {
		return nil, fmt.Errorf("ppmd8: decode: %w", err)
	}
	if len(res) != 1 {
		return nil, errors.New("ppmd8: unexpected wasm result")
	}
	switch int32(res[0]) {
	case statusOK:
	case statusAllocFailed:
		return nil, errors.New("ppmd8: model allocation failed")
	case statusRangeInit:
		return nil, ErrCorrupt
	case statusCorrupt:
		return nil, ErrCorrupt
	case statusOverrun:
		return nil, ErrCorrupt
	case statusBadParams:
		return nil, ErrUnsupported
	default:
		return nil, fmt.Errorf("ppmd8: decoder status %d", int32(res[0]))
	}

	out, ok := inst.Memory().Read(outPtr, uint32(size))
	if !ok {
		return nil, errors.New("ppmd8: read output from wasm memory")
	}
	// Read returns a view into wasm memory; copy it before the instance closes.
	buf := make([]byte, len(out))
	copy(buf, out)
	return buf, nil
}

func wasmAlloc(ctx context.Context, malloc api.Function, size int) (uint32, error) {
	if size <= 0 {
		size = 1
	}
	res, err := malloc.Call(ctx, uint64(size))
	if err != nil {
		return 0, fmt.Errorf("ppmd8: malloc: %w", err)
	}
	if len(res) != 1 || res[0] == 0 {
		return 0, errors.New("ppmd8: wasm out of memory")
	}
	return uint32(res[0]), nil
}
