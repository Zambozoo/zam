#pragma once

#include "common.h"

// The opcodes, in the order the compiler numbers them, which is the order of
// the const block in internal/4_codegen/3_operation.go. An opcode's value is
// its position in that block and nothing else, so the two lists agreeing is the
// whole of the contract: an entry inserted there and not here does not fail to
// build, it silently shifts every opcode after it.
//
// The names are the compiler's own identifiers rather than a C spelling of
// them, so that one grep finds both sides of any instruction.
//
// The second column is how many immediate operands follow the opcode byte, each
// a word. It is what the loop advances the program counter by, and it is read
// off the same `(code, ...)` line in the Go comment that documents the
// instruction, so it moves when that documentation does.
#define ZAMFOREACH_OPCODE(OP)                                                 \
    OP(OpDupWord, 1)                                                           \
                                                                               \
    OP(OpPushI64, 1)                                                           \
    OP(OpIsNegativeI64, 0)                                                     \
    OP(OpIsPositiveI64, 0)                                                     \
                                                                               \
    OP(OpAddI64, 0)                                                            \
    OP(OpSubI64, 0)                                                            \
    OP(OpMulI64, 0)                                                            \
    OP(OpDivI64, 0)                                                            \
    OP(OpModI64, 0)                                                            \
                                                                               \
    OP(OpArithmeticNegationI64, 0)                                             \
    OP(OpBitwiseNegationI64, 0)                                                \
                                                                               \
    OP(OpAndI64, 0)                                                            \
    OP(OpOrI64, 0)                                                             \
    OP(OpXorI64, 0)                                                            \
                                                                               \
    OP(OpEqualI64, 0)                                                          \
                                                                               \
    OP(OpCastToString, 0)                                                      \
    OP(OpCastI64ToF64, 0)                                                      \
    OP(OpCastF64ToI64, 0)                                                      \
                                                                               \
    OP(OpAddF64, 0)                                                            \
    OP(OpSubF64, 0)                                                            \
    OP(OpMulF64, 0)                                                            \
    OP(OpDivF64, 0)                                                            \
                                                                               \
    OP(OpNegF64, 0)                                                            \
    OP(OpEqualF64, 0)                                                          \
                                                                               \
    OP(OpShiftLeftI64, 0)                                                      \
    OP(OpShiftRightI64, 0)                                                     \
    OP(OpShiftArithmeticRightI64, 0)                                           \
                                                                               \
    OP(OpPushGenericArgType, 1)                                                \
                                                                               \
    OP(OpReturn, 0)                                                            \
    OP(OpReturnVoid, 0)                                                        \
    OP(OpExit, 0)                                                              \
                                                                               \
    OP(OpJumpIfFalse, 1)                                                       \
    OP(OpJump, 1)                                                              \
                                                                               \
    OP(OpPop, 0)                                                               \
    OP(OpPopWord, 0)                                                           \
    OP(OpPopMark, 0)                                                           \
    OP(OpMark, 0)                                                              \
                                                                               \
    OP(OpPushScope, 0)                                                         \
    OP(OpPopScope, 0)                                                          \
                                                                               \
    OP(OpPushMember, 1)                                                        \
    OP(OpLoadTailMember, 1)                                                    \
    OP(OpPushMethod, 1)                                                        \
    OP(OpCallMethod, 1)                                                        \
                                                                               \
    OP(OpEq, 0)                                                                \
    OP(OpCmp, 0)                                                               \
                                                                               \
    OP(OpNew, 0)                                                               \
    OP(OpNewArray, 1)                                                          \
                                                                               \
    OP(OpNewThread, 1)                                                         \
    OP(OpWaitThreadVoid, 0)                                                    \
    OP(OpWaitThread, 0)                                                        \
                                                                               \
    OP(OpStore, 0)                                                             \
    OP(OpLoad, 0)                                                              \
                                                                               \
    OP(OpPushStatic, 2)                                                        \
    OP(OpPushGenericStaticSlot, 1)                                             \
    OP(OpPushStringLiteral, 1)                                                 \
                                                                               \
    OP(OpDelete, 0)                                                            \
    OP(OpFree, 0)                                                              \
    OP(OpHash, 0)                                                              \
                                                                               \
    OP(OpPushInterfaceMethod, 1)                                               \
                                                                               \
    OP(OpPushNull, 0)                                                          \
    OP(OpIncPre, 0)                                                            \
    OP(OpDecPre, 0)                                                            \
    OP(OpIncPost, 0)                                                           \
    OP(OpDecPost, 0)                                                           \
                                                                               \
    OP(OpTypeOf, 0)                                                            \
    OP(OpSizeOf, 0)                                                            \
                                                                               \
    OP(OpConcatString, 0)                                                      \
    OP(OpNative, 1)                                                            \
                                                                               \
    OP(OpIsSubtypeOf, 0)                                                       \
    OP(OpAddressOf, 0)                                                         \
                                                                               \
    OP(OpNewLambda, 2)                                                         \
    OP(OpPushVariable, 1)                                                      \
    OP(OpDereference, 0)                                                       \
    OP(OpDefaultValue, 0)                                                      \
    OP(OpPushReceiver, 0)                                                      \
                                                                               \
    OP(OpCallNative, 1)                                                        \
                                                                               \
    OP(OpPushReceiverType, 1)                                                  \
                                                                               \
    OP(OpBoxAny, 0)                                                            \
    OP(OpAnyPayloadType, 0)                                                    \
    OP(OpAnyUnbox, 0)

#define ZAMOPCODE_ENUM(name, operands) name,

typedef enum ZamOpcode {
    ZAMFOREACH_OPCODE(ZAMOPCODE_ENUM) ZAMOPCODE_COUNT
} ZamOpcode;

// An opcode is one byte on the wire, so the enum has to fit in one for a fetch
// to be a load rather than a range check. This is a ceiling on the instruction
// set, and reaching it is a decision to widen the encoding.
_Static_assert(ZAMOPCODE_COUNT <= 256, "opcodes no longer fit in a byte");

// zam_opcode_name is the compiler's identifier for the opcode, for the one
// audience that reads it: a trace, and the message a stub or a bad byte dies
// with.
const char *zam_opcode_name(ZamOpcode opcode);

// zam_opcode_operands is how many immediate words follow the opcode byte.
uint8_t zam_opcode_operands(ZamOpcode opcode);
