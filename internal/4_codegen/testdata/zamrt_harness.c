// A harness over the runtime as compiled code calls it, zamrt.h.

#include <inttypes.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>

#include "harness_image.h"
#include "run.h"
#include "zamrt.h"

// The project's own natives, of which the harness has none.
const ZamNativeEntry zamProjectNativeTable[] = {{NULL, NULL}};
const uint64_t zamProjectNativeTableCount = 0;

static void put(const char *name, uint64_t value) {
    printf("%s=%" PRIu64 "\n", name, value);
}

// pointeeOfRoot answers the type the index'th static root's pointer points at.
static ZamTypeID pointeeOfRoot(ZamVM *vm, uint64_t index) {
    ZamTypeID type;
    zam_image_static_root(&vm->image, index, &type);
    return zam_pointer_pointee(zam_pointer_definition(&vm->image, ZAMTYPE_DECL(type.shortID)));
}

// frames enters two frames over slots that start dirty, and leaves them.
static void frames(ZamVM *vm) {
    ZamThread *thread = &vm->main;
    static const ZamFrameInfo outerInfo = {.functionID = 1, .slotCount = 2};
    static const ZamFrameInfo innerInfo = {.functionID = 2, .slotCount = 3};
    _Alignas(ZamFrame) uint8_t outerBytes[sizeof(ZamFrame) + 3 * sizeof(ZamWord)];
    _Alignas(ZamFrame) uint8_t innerBytes[sizeof(ZamFrame) + 3 * sizeof(ZamWord)];
    memset(outerBytes, 0xab, sizeof outerBytes);
    memset(innerBytes, 0xab, sizeof innerBytes);
    ZamFrame *outer = (ZamFrame *)(void *)outerBytes;
    ZamFrame *inner = (ZamFrame *)(void *)innerBytes;

    put("empty", thread->frames == NULL);
    zamrt_frame_enter(thread, outer, &outerInfo);
    zamrt_frame_enter(thread, inner, &innerInfo);
    put("innermost", thread->frames == inner);
    put("caller", inner->caller == outer);
    put("outerCaller", outer->caller == NULL);
    put("zeroed", inner->slots[0] == 0 && inner->slots[2] == 0 && outer->slots[1] == 0);
    put("pastSlots", outer->slots[2] == 0xababababababababULL);
    zamrt_frame_leave(thread, inner);
    put("left", thread->frames == outer);
    zamrt_frame_leave(thread, outer);
    put("leftAll", thread->frames == NULL);
}

// classRoot answers the type of whichever static root points at a class.
static ZamTypeID classRoot(ZamVM *vm) {
    ZamTypeID first = pointeeOfRoot(vm, 0);
    return zam_type_is_class(&vm->image, first.shortID) ? first : pointeeOfRoot(vm, 1);
}

// alloc allocates a run of boxes, writes into the second, and records it.
static void alloc(ZamVM *vm) {
    ZamThread *thread = &vm->main;
    ZamTypeID box = classRoot(vm);
    uint64_t bytes = zamrt_size_of(&vm->image, box);
    put("sized", bytes > 0);

    put("none", zamrt_new(thread, box, 0) == NULL);
    uint8_t *run = zamrt_new(thread, box, 3);
    const ZamTypedObjectHeader *header = (const ZamTypedObjectHeader *)run;
    put("shortID", header->type.shortID == box.shortID);
    put("elementCount", header->type.elementCount);

    zam_heap_clear_cards(&vm->heap);
    uint8_t *second = run + bytes;
    uint64_t card = ((uintptr_t)second - (uintptr_t)vm->heap.base) >> ZAMHEAP_CARD_SHIFT;
    put("cleanBefore", vm->heap.cards[card] == 0);
    zamrt_write_barrier(thread, second, ZAMWORD_BYTES);
    put("dirtyAfter", vm->heap.cards[card] != 0);

    ZamString *string = zamrt_new_string(thread, "hi\0there", 8);
    put("stringLength", string->length);
    put("stringBytes", memcmp(string->bytes, "hi\0there", 8) == 0);
}

// types asks about the two classes the static roots point at, one extending
// the other.
static void types(ZamVM *vm) {
    ZamTypeID a = pointeeOfRoot(vm, 0);
    ZamTypeID b = pointeeOfRoot(vm, 1);
    put("reflexive", zamrt_is_subtype(&vm->image, a, a) && zamrt_is_subtype(&vm->image, b, b));
    put("oneWay", zamrt_is_subtype(&vm->image, a, b) != zamrt_is_subtype(&vm->image, b, a));
}

// poll asks for a collection, which a poll answers.
static void poll(ZamVM *vm) {
    ZamThread *thread = &vm->main;
    uint64_t before = vm->collections;
    zamrt_poll(thread);
    put("idle", vm->collections == before);
    atomic_store(&vm->collectionRequested, true);
    zamrt_poll(thread);
    put("collected", vm->collections == before + 1);
}

// newBox allocates a box of type box holding n in its first member.
static void *newBox(ZamVM *vm, ZamTypeID box, ZamInt n) {
    uint8_t *object = zamrt_new(&vm->main, box, 1);
    ZamMemberDefinition member = zam_type_member(&vm->image, box.shortID, 0);
    memcpy(object + zam_value_header_bytes(&vm->image, box) + member.offset, &n, sizeof n);
    return object;
}

// putString reports a string's text, or that there is none.
static void putString(const char *name, const ZamString *string) {
    if (string == NULL)
        printf("%s=<null>\n", name);
    else
        printf("%s=%.*s\n", name, (int)string->length, string->bytes);
}

// boxVM and boxType are the VM and box type the box methods below read a box by.
static ZamVM *boxVM;
static ZamTypeID boxType;

// boxN is the n a box holds.
static ZamInt boxN(ZamWord box) {
    ZamMemberDefinition member = zam_type_member(&boxVM->image, boxType.shortID, 0);
    ZamInt n;
    memcpy(&n, (uint8_t *)(uintptr_t)box + zam_value_header_bytes(&boxVM->image, boxType) + member.offset,
           sizeof n);
    return n;
}

// boxEq, boxCmp, boxHash and boxStr are the box's own Eq, Cmp, Hash and Str,
// as compiled code would enter them: by n modulo ten, and as "box" and n.
static void boxEq(ZamThread *thread, ZamClosure *self, ZamWord *args, ZamWord *result) {
    (void)thread, (void)self;
    *result = boxN(args[0]) % 10 == boxN(args[1]) % 10;
}

static void boxCmp(ZamThread *thread, ZamClosure *self, ZamWord *args, ZamWord *result) {
    (void)thread, (void)self;
    *result = (ZamWord)(boxN(args[0]) % 10 - boxN(args[1]) % 10);
}

static void boxHash(ZamThread *thread, ZamClosure *self, ZamWord *args, ZamWord *result) {
    (void)thread, (void)self;
    *result = (ZamWord)(boxN(args[0]) % 10);
}

static void boxStr(ZamThread *thread, ZamClosure *self, ZamWord *args, ZamWord *result) {
    (void)self;
    char text[32];
    int length = snprintf(text, sizeof text, "box%" PRId64, (int64_t)boxN(args[0]));
    *result = (ZamWord)(uintptr_t)zamrt_new_string(thread, text, (uint64_t)length);
}

// compileBox gives the VM a class table holding the box's own Eq, Cmp, Hash
// and Str, as a compiled program's holds them.
static void compileBox(ZamVM *vm, ZamTypeID box) {
    static ZamEntry methods[ZAMOBJECT_SLOT_STR + 1];
    static ZamCompiledClass classes[1];
    boxVM = vm;
    boxType = box;
    methods[ZAMOBJECT_SLOT_EQ] = boxEq;
    methods[ZAMOBJECT_SLOT_CMP] = boxCmp;
    methods[ZAMOBJECT_SLOT_HASH] = boxHash;
    methods[ZAMOBJECT_SLOT_STR] = boxStr;
    classes[0] = (ZamCompiledClass){.shortID = box.shortID, .methodCount = ZAMOBJECT_SLOT_STR + 1, .methods = methods};
    vm->compiledClasses = classes;
    vm->compiledClassCount = 1;
}

// values asks after primitives, strings and boxes whose class overrides Eq,
// Cmp, Hash and Str.
static void values(ZamVM *vm) {
    ZamThread *thread = &vm->main;
    ZamTypeID ints = {.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_INT, 0)};
    ZamTypeID strs = {.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_STRING, 0)};
    ZamTypeID boxes;
    zam_image_static_root(&vm->image, 0, &boxes);
    ZamTypeID box = pointeeOfRoot(vm, 0);
    ZamWord intWords[ZAMTYPE_ID_WORDS], strWords[ZAMTYPE_ID_WORDS], boxWords[ZAMTYPE_ID_WORDS];
    compileBox(vm, box);
    zam_type_id_write(intWords, ints);
    zam_type_id_write(strWords, strs);
    zam_type_id_write(boxWords, boxes);

    ZamInt seven = 7, otherSeven = 7, three = 3, nine = 9, negative = -42;
    put("int.eq", zamrt_eq(thread, intWords, &seven, intWords, &otherSeven));
    put("int.ne", zamrt_eq(thread, intWords, &seven, intWords, &three));
    put("int.cmp", (uint64_t)(zamrt_cmp(thread, intWords, &three, intWords, &nine) + 10));
    put("int.hash", zamrt_hash(thread, intWords, &seven) == zamrt_hash(thread, intWords, &otherSeven));
    putString("int.str", zamrt_str(thread, intWords, &negative));

    ZamWord ab = (ZamWord)(uintptr_t)zamrt_new_string(thread, "ab", 2);
    ZamWord otherAb = (ZamWord)(uintptr_t)zamrt_new_string(thread, "ab", 2);
    ZamWord b = (ZamWord)(uintptr_t)zamrt_new_string(thread, "b", 1);
    put("str.eq", zamrt_eq(thread, strWords, &ab, strWords, &otherAb));
    put("str.cmp", (uint64_t)(zamrt_cmp(thread, strWords, &ab, strWords, &b) + 10));
    put("str.hash", zamrt_hash(thread, strWords, &ab) == zamrt_hash(thread, strWords, &otherAb));
    put("str.str", zamrt_str(thread, strWords, &ab) == (ZamString *)(uintptr_t)ab);

    ZamWord thirteen = (ZamWord)(uintptr_t)newBox(vm, box, 13);
    ZamWord twentyThree = (ZamWord)(uintptr_t)newBox(vm, box, 23);
    ZamWord five = (ZamWord)(uintptr_t)newBox(vm, box, 5);
    ZamWord null = 0;
    put("box.eq", zamrt_eq(thread, boxWords, &thirteen, boxWords, &twentyThree));
    put("box.ne", zamrt_eq(thread, boxWords, &thirteen, boxWords, &five));
    put("box.nullEq", zamrt_eq(thread, boxWords, &null, boxWords, &null));
    put("box.nullNe", zamrt_eq(thread, boxWords, &null, boxWords, &five));
    put("box.cmp", (uint64_t)(zamrt_cmp(thread, boxWords, &thirteen, boxWords, &five) + 10));
    put("box.hash", zamrt_hash(thread, boxWords, &twentyThree));
    putString("box.str", zamrt_str(thread, boxWords, &thirteen));
    putString("box.nullStr", zamrt_str(thread, boxWords, &null));
    put("returned", thread->functionEntry == ZAMMARK_NONE);
}

// rootInBucket answers the pointer type of whichever static root points at a
// declaration in bucket.
static ZamTypeID rootInBucket(ZamVM *vm, ZamTypeBucket bucket) {
    for (uint64_t index = 0; index < vm->image.staticRootCount; index++) {
        ZamTypeID type;
        zam_image_static_root(&vm->image, index, &type);
        if (zam_image_bucket_of(&vm->image, ZAMTYPE_DECL(pointeeOfRoot(vm, index).shortID)) == bucket)
            return type;
    }
    fprintf(stderr, "no root in bucket %d\n", (int)bucket);
    exit(2);
}

// pointee answers the type a pointer type points at.
static ZamTypeID pointee(ZamVM *vm, ZamTypeID pointer) {
    return zam_pointer_pointee(zam_pointer_definition(&vm->image, ZAMTYPE_DECL(pointer.shortID)));
}

// dispatch asks a box its type and where the methods of the abstract it
// extends and the interface it implements lie among its own.
static void dispatch(ZamVM *vm) {
    ZamThread *thread = &vm->main;
    ZamTypeID boxes = rootInBucket(vm, ZAMBUCKET_CLASS);
    ZamTypeID bases = rootInBucket(vm, ZAMBUCKET_ABSTRACT);
    ZamTypeID shapes = rootInBucket(vm, ZAMBUCKET_INTERFACE);
    ZamTypeShort box = pointee(vm, boxes).shortID;
    ZamTypeShort base = pointee(vm, bases).shortID;

    ZamWord instance = (ZamWord)(uintptr_t)zamrt_new(thread, pointee(vm, boxes), 1);
    put("typeOf", zamrt_type_of(thread, (void *)(uintptr_t)instance).shortID == box);

    // Area is the last method the abstract declares, and has no body of its
    // own to be found there.
    uint32_t overridden = (uint32_t)zam_type_method_count(&vm->image, base) - 1;
    put("overrides", overridden < zam_type_method_count(&vm->image, box));

    // The interface's one method is one of the box's own.
    uint32_t index = zam_interface_method_index(&vm->image, box, pointee(vm, shapes).shortID, 0);
    put("interface", index < zam_type_method_count(&vm->image, box));
}

// anys boxes an int and a box, reads back what each holds, and unboxes it.
static void anys(ZamVM *vm) {
    ZamThread *thread = &vm->main;
    ZamTypeID ints = {.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_INT, 0)};
    ZamTypeID boxes = rootInBucket(vm, ZAMBUCKET_CLASS);
    ZamWord intWords[ZAMTYPE_ID_WORDS], boxWords[ZAMTYPE_ID_WORDS], uintWords[ZAMTYPE_ID_WORDS];
    zam_type_id_write(intWords, ints);
    zam_type_id_write(boxWords, boxes);
    zam_type_id_write(uintWords, (ZamTypeID){.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_UINT, 0)});

    ZamInt seven = 7;
    void *anInt = zamrt_any_box(thread, intWords, &seven);
    seven = 0;
    ZamTypeID held = zamrt_any_type(thread, anInt);
    put("int.type", held.shortID == ints.shortID && held.arrayLength == 0);
    ZamInt out = 0;
    zamrt_any_unbox(thread, anInt, intWords, &out);
    put("int.value", (uint64_t)out);

    ZamWord box = (ZamWord)(uintptr_t)zamrt_new(thread, pointee(vm, boxes), 1);
    void *aBox = zamrt_any_box(thread, boxWords, &box);
    put("box.type", zamrt_any_type(thread, aBox).shortID == boxes.shortID);
    ZamWord unboxed = 0;
    zamrt_any_unbox(thread, aBox, boxWords, &unboxed);
    put("box.value", unboxed == box);
    fflush(stdout);

    ZamUint wrong = 0;
    zamrt_any_unbox(thread, anInt, uintWords, &wrong);
    put("unreached", 1);
}

int main(int argc, char **argv) {
    if (argc != 3) {
        fprintf(stderr, "usage: zamrt_harness <case> <image>\n");
        return 2;
    }

    size_t byteCount;
    uint8_t *image = readImage(argv[2], &byteCount);
    ZamVM vm;
    zam_vm_init(&vm, image, byteCount);
    if (strcmp(argv[1], "frames") == 0)
        frames(&vm);
    else if (strcmp(argv[1], "alloc") == 0)
        alloc(&vm);
    else if (strcmp(argv[1], "types") == 0)
        types(&vm);
    else if (strcmp(argv[1], "poll") == 0)
        poll(&vm);
    else if (strcmp(argv[1], "values") == 0)
        values(&vm);
    else if (strcmp(argv[1], "dispatch") == 0)
        dispatch(&vm);
    else if (strcmp(argv[1], "anys") == 0)
        anys(&vm);
    else if (strcmp(argv[1], "panic") == 0)
        zamrt_panic(&vm.main, ZAMPANIC_DIVIDE_BY_ZERO);
    else if (strcmp(argv[1], "exit") == 0)
        zamrt_exit(&vm.main, 300);
    else {
        fprintf(stderr, "unknown case %s\n", argv[1]);
        return 2;
    }
    fflush(stdout);
    zam_vm_free(&vm);
    free(image);
    return 0;
}
