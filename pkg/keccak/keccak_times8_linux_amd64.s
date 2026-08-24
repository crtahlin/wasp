//go:build amd64 && !purego

#include "textflag.h"
#include "funcdata.h"

// func keccak256x8asm(inputs *[8][]byte, outputs *[8]Hash256, scratch unsafe.Pointer) uintptr
//
// Runs the XKCP blob on a caller-supplied scratch stack rather than on the
// goroutine stack, and returns 0 if the blob left the stack pointer where it
// found it.
//
// The blob is foreign machine code. Executing it on a goroutine stack corrupts
// memory: Go's stacks are small, growable and movable, and the runtime cannot
// reason about non-Go code running on one. Measured on a mainnet node, the
// previous version crashed within ~12 minutes under load, while the same code
// invoked through cgo — which switches to the system stack — ran 64 minutes
// clean. See issue #92.
//
// All arguments are read into registers BEFORE the switch. FP is expressed
// relative to the hardware stack pointer, so argument references are invalid
// once SP has moved.
//
// The goroutine SP is saved twice, in BX and on the scratch stack itself, and
// the two copies are compared after the call. Each copy assumes a different
// clause of the SysV ABI: the register copy assumes the blob preserves BX, the
// memory copy assumes it never writes above its own stack pointer. Both hold
// for the blobs shipped here — their prologues push BX, and neither contains
// any register-based stack adjustment — but the premise of this whole fix is
// that the blob is not to be trusted, so neither assumption is load-bearing on
// its own. A disagreement means the blob violated the ABI, and is reported to
// the caller, which panics rather than continuing on a stack of unknown
// provenance.
//
// NOSPLIT guarantees no stack-growth prologue is emitted, and so no path from
// here into morestack, whatever frame size this function is later given. It is
// belt-and-braces: such a check would run at entry, before SP moves. The load
// -bearing protection is separate and comes for free — the runtime refuses to
// asynchronously preempt or stack-scan an assembly function with no valid Go
// frame, so the collector never walks a goroutine whose SP points into a heap
// buffer.
//
// The frame is declared $0 but is not zero in the emitted code: because this
// function contains a CALL, the assembler inserts a BP push at entry and a pop
// before RET. Argument offsets account for it, and the SP save and restore
// bracket it symmetrically, so it is harmless.
TEXT ·keccak256x8asm(SB), NOSPLIT, $0-32
	NO_LOCAL_POINTERS

	MOVQ inputs+0(FP), DI
	MOVQ outputs+8(FP), SI
	MOVQ scratch+16(FP), AX

	MOVQ SP, BX             // first copy of the goroutine SP: a callee-saved register
	ANDQ $~63, AX           // 64-byte align; rounds down, so it stays inside the buffer
	MOVQ BX, (AX)           // second copy, at the top of the scratch stack
	MOVQ AX, SP             // switch to the scratch stack

	CALL go_keccak256x8(SB)

	MOVQ (SP), CX           // what the memory copy says
	MOVQ BX, SP             // restore from the register copy
	SUBQ BX, CX             // 0 when the two agree
	MOVQ CX, ret+24(FP)
	RET
