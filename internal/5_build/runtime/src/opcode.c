#include "opcode.h"

#define ZAMOPCODE_NAME(name, operands) #name,
#define ZAMOPCODE_OPERANDS(name, operands) operands,

static const char *const opcodeNames[] = {ZAMFOREACH_OPCODE(ZAMOPCODE_NAME)};
static const uint8_t opcodeOperands[] = {
    ZAMFOREACH_OPCODE(ZAMOPCODE_OPERANDS)};

const char *zam_opcode_name(ZamOpcode opcode) {
    return opcode < ZAMOPCODE_COUNT ? opcodeNames[opcode] : "<unknown opcode>";
}

uint8_t zam_opcode_operands(ZamOpcode opcode) {
    // An opcode out of range carries no operands as far as this can say, and
    // the loop rejects it before it would act on the answer.
    return opcode < ZAMOPCODE_COUNT ? opcodeOperands[opcode] : 0;
}
