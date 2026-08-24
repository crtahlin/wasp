//go:build amd64 && !purego

#include "textflag.h"
#include "funcdata.h"

// func keccak256x8asm(inputs *[8][]byte, outputs *[8]Hash256, scratch unsafe.Pointer)
//
// Runs the XKCP blob on a caller-supplied scratch stack rather than on the
// goroutine stack.
//
// The blob is foreign machine code. Executing it on a goroutine stack corrupts
// memory: Go's stacks are small, growable and movable, and the runtime cannot
// reason about non-Go code running on one. Measured on a mainnet node, the
// previous version crashed within ~12 minutes under load, while the same code
// invoked through cgo — which switches to the system stack — ran 64 minutes
// clean. See issue #92.
//
// NOSPLIT and a zero frame: no stack growth check may run here, because the
// stack pointer is not the goroutine's for most of this function.
//
// All arguments are read into registers BEFORE the switch. FP is expressed
// relative to the hardware stack pointer, so argument references are invalid
// once SP has moved.
//
// BX holds the saved goroutine SP across the call. BX is callee-saved in the
// SysV ABI and the blob's prologue pushes it, so it survives.
TEXT ·keccak256x8asm(SB), NOSPLIT, $0-24
	NO_LOCAL_POINTERS

	MOVQ inputs+0(FP), DI
	MOVQ outputs+8(FP), SI
	MOVQ scratch+16(FP), AX

	MOVQ SP, BX             // save the goroutine stack pointer
	ANDQ $~63, AX           // 64-byte align for AVX-512 loads
	MOVQ AX, SP             // switch to the scratch stack

	CALL go_keccak256x8(SB)

	MOVQ BX, SP             // restore the goroutine stack pointer
	RET
