//go:build windows

#include "textflag.h"

// Two methods of the providers take doubles, which the x64 calling convention passes in
// XMM registers: a Go callback (syscall.NewCallback) reads integer registers and the
// stack only. Each thunk copies the doubles' bits into the integer registers of the same
// argument positions and jumps to its Go callback, whose address is in a Go variable. It
// touches neither the stack nor the callee-saved registers, so the callback finds the
// call exactly as the caller made it, its doubles now as uintptr bits.

// fromPointThunk is IRawElementProviderFragmentRoot::ElementProviderFromPoint(this,
// double x, double y, IRawElementProviderFragment **): x in XMM1, y in XMM2.
TEXT ·fromPointThunk(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ X1, DX
	MOVQ X2, R8
	MOVQ ·fromPointTarget(SB), AX
	JMP  AX

// setRangeThunk is IRangeValueProvider::SetValue(this, double val): val in XMM1.
TEXT ·setRangeThunk(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ X1, DX
	MOVQ ·setRangeTarget(SB), AX
	JMP  AX

// thunkPCs returns the thunks' entry addresses, as foreign code calls them.
TEXT ·thunkPCs(SB), NOSPLIT, $0-16
	LEAQ ·fromPointThunk(SB), AX
	MOVQ AX, fromPoint+0(FP)
	LEAQ ·setRangeThunk(SB), AX
	MOVQ AX, setRange+8(FP)
	RET
