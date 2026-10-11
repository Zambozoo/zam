#include "instance.h"

// The two switches below are the runtime's whole account of how wide a
// primitive is. Nothing in the image restates it, so these and
// primitiveByteInfo in 2_staticfields.go are a hand-kept pair like the opcode
// table — and unlike the opcode table, a disagreement here is silent rather
// than fatal: a member read one byte too narrow still reads.
//
// They are switches over the enum with no default, so a primitive added to
//ZamPrimitive without a width fails the build rather than falling through to
// one it was never given.

uint64_t zam_primitive_bytes(ZamPrimitive primitive) {
    switch (primitive) {
    case ZAMPRIMITIVE_VOID:
        return ZAMVOID_BYTES;
    case ZAMPRIMITIVE_BOOL:
        return ZAMBOOL_BYTES;
    case ZAMPRIMITIVE_CHAR:
        return ZAMCHAR_BYTES;
    case ZAMPRIMITIVE_BYTE:
        return ZAMBYTE_BYTES;
    case ZAMPRIMITIVE_INT:
        return ZAMINT_BYTES;
    case ZAMPRIMITIVE_FLOAT:
        return ZAMFLOAT_BYTES;
    case ZAMPRIMITIVE_STRING:
        return ZAMSTRING_BYTES;
    case ZAMPRIMITIVE_TYPE:
        return ZAMTYPE_BYTES;
    case ZAMPRIMITIVE_ANY:
        return ZAMANY_BYTES;
    case ZAMPRIMITIVE_UINT:
        return ZAMUINT_BYTES;
    case ZAMPRIMITIVE_COUNT:
        break;
    }
    zam_die(ZAMEXIT_IMAGE, "no primitive is numbered %u\n",
            (unsigned)primitive);
}

uint64_t zam_primitive_align(ZamPrimitive primitive) {
    switch (primitive) {
    case ZAMPRIMITIVE_VOID:
        return ZAMVOID_ALIGN;
    case ZAMPRIMITIVE_BOOL:
        return ZAMBOOL_ALIGN;
    case ZAMPRIMITIVE_CHAR:
        return ZAMCHAR_ALIGN;
    case ZAMPRIMITIVE_BYTE:
        return ZAMBYTE_ALIGN;
    case ZAMPRIMITIVE_INT:
        return ZAMINT_ALIGN;
    case ZAMPRIMITIVE_FLOAT:
        return ZAMFLOAT_ALIGN;
    case ZAMPRIMITIVE_STRING:
        return ZAMSTRING_ALIGN;
    case ZAMPRIMITIVE_TYPE:
        return ZAMTYPE_ALIGN;
    case ZAMPRIMITIVE_ANY:
        return ZAMANY_ALIGN;
    case ZAMPRIMITIVE_UINT:
        return ZAMUINT_ALIGN;
    case ZAMPRIMITIVE_COUNT:
        break;
    }
    zam_die(ZAMEXIT_IMAGE, "no primitive is numbered %u\n",
            (unsigned)primitive);
}
