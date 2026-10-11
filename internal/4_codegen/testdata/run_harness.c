// A harness over the interpreter loop, which is the one part of the runtime
// that cannot be checked by reading a fixture: an instruction's meaning is what
// it did to the stacks and to memory, and none of that is written down anywhere
// to be compared against.
//
// So this runs a whole image and then reports what the run left behind. The
// image is built by the test rather than by the compiler front end, which keeps
// what is being checked to the runtime's reading of the operations: a change
// in how a lambda is emitted moves the fixture and not the expectation.

#include <inttypes.h>
#include <stdio.h>

#include "definition.h"
#include "instance.h"
#include "vm.h"

// The runtime looks these up when it binds the image's natives to C
// functions. A harness carries none of its own, same as the standalone
// runner, so an empty table is enough: the sentinel entry keeps the array
// standard-compliant and the count stays zero so the walk never reaches it.
const ZamNativeEntry zamProjectNativeTable[] = {{NULL, NULL}};
const uint64_t zamProjectNativeTableCount = 0;

static void put(const char *name, uint64_t value) {
    printf("%s=%" PRIu64 "\n", name, value);
}

// What a run of markable values behind one pointer says about itself, which is
// almost nothing said by the elements: the front element is the array, so it is
// the one that carries how many there are, and the rest carry how far back it
// lies. Reaching the length from an element past the front is therefore a step
// to the front and a read there.
//
// The front element's upvalue is not reported, because it is the one number here
// that is two different things. On an allocation it is the sweep-list link and
// on a stack value it is zero, and the contained bit already says which — so a
// reporter that printed it could not be asserted against for both.
//
// The stride comes from the definition rather than from a number written here,
// which is what makes an element found at the wrong distance visible.
static void putArray(const ZamImage *image, uint8_t *first) {
    uint64_t count = zam_object_header_of(first)->elementCount;
    put("array.count", count);
    put("array.contained", zam_mark_word_of(first)->contained);

   ZamTypeID element = {
        .shortID = (ZamTypeShort)zam_object_header_of(first)->shortID,
    };
    uint64_t stride = zam_type_bytes(image, element);

    // The count is what the loop runs to and is also what is under test, so it
    // is capped: a count that came back wrong is already a failed assertion
    // above, and without a ceiling a wrong one would walk off the end and take
    // the buffered output with it before any of it was read.
    for (uint64_t index = 1; index < count && index < 8; index++) {
        uint8_t *at = first + index * stride;
        char name[64];
        snprintf(name, sizeof name, "element.%" PRIu64 ".contained", index);
        put(name, zam_mark_word_of(at)->contained);
        snprintf(name, sizeof name, "element.%" PRIu64 ".upvalue", index);
        put(name, zam_mark_word_of(at)->upvalue);
        // One, and the front element's count is the array's. An element that
        // answered for the whole run would make the length depend on which
        // element was asked.
        snprintf(name, sizeof name, "element.%" PRIu64 ".elementCount", index);
        put(name, zam_object_header_of(at)->elementCount);
        snprintf(name, sizeof name, "element.%" PRIu64 ".containerIsFirst",
                 index);
        put(name, zam_container_of(at) == first);
    }
}

int main(int argc, char **argv) {
    if (argc != 3)
        zam_die(ZAMEXIT_USAGE, "usage: %s <subject> <image>\n", argv[0]);

    // Every program is asked how it stopped and what it left on its stacks.
    // Only one that was meant to build a closure is asked about one, since a
    // program that never allocated has nothing in the slot to read.
    bool wantClosure = strcmp(argv[1], "closure") == 0;

    // A program that was meant to allocate is asked about the heap instead.
    // Nothing about the sweep list is reachable from inside a program — it is
    // the collector's own bookkeeping and no instruction reads it — so this is
    // the only side from which it can be looked at.
    bool wantHeap = strcmp(argv[1], "heap") == 0;

    // A program that was meant to leave a markable value standing is asked what
    // the headers inside it say. This is the only side that can be asked: a
    // header is read through a value rather than pushed, so no instruction
    // returns one, and the exit status is a single byte and could not carry a
    // back-distance even where an instruction did.
    bool wantInner = strcmp(argv[1], "inner") == 0;

    // The same question asked of a run of values rather than of one, on either
    // side of the split that decides what the front element's mark word means:
    // an array standing in a frame slot, and an array the allocator handed out.
    bool wantArray = strcmp(argv[1], "array") == 0;
    bool wantHeapArray = strcmp(argv[1], "heaparray") == 0;

    // And of a run that is somebody's member rather than a value of its own,
    // which is the third place the same headers have to appear. The run is
    // reached through the member record, so the length the record carries is
    // what has to have been read for there to be more than one element to look
    // at — and nothing else in the record says there is.
    bool wantMemberArray = strcmp(argv[1], "memberarray") == 0;

    // A program that was meant to leave a type id standing is asked what the id
    // says. This is the only side that can be asked: an id is three words and
    // the exit status is one byte, so a program cannot report one about itself,
    // and no instruction turns an id back into something narrower.
    bool wantType = strcmp(argv[1], "type") == 0;

    // A program that was meant to leave a mark entry standing is asked what the
    // entry says. This is the only side that can be asked: the mark stack is the
    // collector's own and no instruction reads a word off it, so a program
    // cannot report on one about itself.
    //
    // markByteCount above is not enough on its own. A run of addresses owes one
    // entry over the whole run, and a count alone cannot tell that from an entry
    // over the front element and nothing behind it — both are one entry, and
    // only the two words the array kind carries say which was pushed.
    bool wantMark = strcmp(argv[1], "mark") == 0;

    // A program whose static region holds a markable value is asked what the
    // region says about it. Nothing runs over the static region before the first
    // instruction — there is no initializer pass for statics — so a header there
    // is one the compiler wrote and the loader copied, and this is the only side
    // that can be asked whether it survived the trip.
    bool wantStatic = strcmp(argv[1], "static") == 0;

   ZamVM vm;
    zam_vm_init(&vm, argv[2]);
   ZamResult result = zam_vm_run(&vm, 0, NULL);

    printf("result=%s\n", zam_result_name(result));
    put("exitStatus", vm.main.exitStatus);

    // What the stacks hold at the end is the whole of what says the
    // instructions spent what they were meant to. A word left standing is an
    // instruction that did not take its operands; a mark entry left standing is
    // a root the collector would start from after the program gave it up.
    //
    // Main's own frame is not counted. The run lays it before the first
    // instruction, so those words are not ones an instruction failed to take.
    put("operandCount", vm.main.operandCount - ZAMMAIN_FRAME_WORDS);
    put("markByteCount", vm.main.markByteCount);

    if (wantClosure) {
        // The static slot is where the prologue was building to. Reading it as
        // a closure is the claim being made: that the pointer OpPushStatic
        // left, the allocation OpNewLambda made and the copy OpStore did all
        // named the same eight bytes.
        //
        // Slot 3, not slot 0: the image these tests build interns three literals,
        // and literals hold the slots that begin the static region. This is
        // mainStaticSlot in run_conformance_test.go and moves with it.
        void *closureWord = *(void **)zam_vm_static(&vm, 3);
        const ZamClosure *closure = zam_closure_of(closureWord);

        put("closure.state", zam_mark_word_of(closureWord)->state);
        put("closure.kind", zam_mark_word_of(closureWord)->kind);
        put("closure.shortID", zam_object_header_of(closureWord)->shortID);
        put("closure.elementCount",
            zam_object_header_of(closureWord)->elementCount);
        put("closure.instruction", closure->instruction);
        put("closure.captureCount", closure->captureCount);

        // A lambda at the top level closes over no receiver, and that is said
        // by a void receiver type rather than by a flag, so the entry being
        // there at all and being void is the part worth reading back.
       ZamTypeID receiver =
            zam_closure_entry_type(zam_closure_first(closure));
        put("closure.receiver.shortID", receiver.shortID);
        put("closure.receiver.arrayLength", receiver.arrayLength);
        put("closure.receiver.tailLength", receiver.tailLength);
    }

    if (wantHeap) {
        // How long the sweep list is, which is the whole claim that an
        // allocation was linked as it was made. A count is enough: an
        // allocation the allocator handed out and did not link is
        // indistinguishable from one it never made, and that is exactly the
        // failure the count catches.
        uint64_t count = 0;
        for (void *object = vm.allocations; object != NULL; count++)
            object = (void *)(uintptr_t)zam_mark_word_of(object)->upvalue;
        put("heap.count", count);

        // The head, which is the most recent allocation because the list is
        // pushed onto rather than appended to. Its header is what says the
        // allocator was told the right kind and the right type.
        if (vm.allocations != NULL) {
            put("heap.state", zam_mark_word_of(vm.allocations)->state);
            put("heap.kind", zam_mark_word_of(vm.allocations)->kind);
            put("heap.shortID", zam_object_header_of(vm.allocations)->shortID);
            put("heap.elementCount",
                zam_object_header_of(vm.allocations)->elementCount);
        }
    }

    if (wantInner) {
        // The value the program left standing, which is the first thing above
        // main's own frame. A frame slot is where a fresh value goes and the
        // slot is these words, so the value being here is the same claim
        // operandCount makes.
        uint8_t *outer = (uint8_t *)(vm.main.operands + ZAMMAIN_FRAME_WORDS);
        put("outer.kind", zam_mark_word_of(outer)->kind);
        put("outer.contained", zam_mark_word_of(outer)->contained);
        put("outer.upvalue", zam_mark_word_of(outer)->upvalue);

        // The member is reached the way OpPushMember reaches one — the header
        // width off the value's own mark word, then the offset the definition
        // records — so a header the runtime got wrong is a header this looks
        // for the inner value past, rather than something restated here and
        // agreed with by accident.
        const ZamMemberDefinition *member = zam_type_member(
            &vm.image, (ZamTypeShort)zam_object_header_of(outer)->shortID, 0);
        uint8_t *inner = outer +
                         zam_object_header_bytes(zam_mark_word_of(outer)->kind) +
                         member->offset;

        put("inner.kind", zam_mark_word_of(inner)->kind);
        put("inner.contained", zam_mark_word_of(inner)->contained);
        put("inner.upvalue", zam_mark_word_of(inner)->upvalue);
        put("inner.shortID", zam_object_header_of(inner)->shortID);

        // The whole of what the back-distance is for: one step out of the value
        // lands on the thing holding it. It is asked through zam_container_of
        // rather than compared by hand, that function being what every reader of
        // an interior value will use and the upvalue above being only the number
        // it reads.
        put("inner.containerIsOuter", zam_container_of(inner) == outer);
    }

    if (wantType) {
        // Read the way an instruction reads one: the three words above main's own
        // frame, decoded through zam_type_id_read rather than picked apart here,
        // so an id written in the wrong order is an id this reports wrongly
        // rather than one it agrees with by restating the same mistake.
       ZamTypeID type =
            zam_type_id_read(vm.main.operands + ZAMMAIN_FRAME_WORDS);
        put("type.shortID", type.shortID);
        put("type.arrayLength", type.arrayLength);
        put("type.tailLength", type.tailLength);
    }

    if (wantStatic) {
        // Slot 4: the literals begin the region and main's closure follows them,
        // so a value of the program's own is the slot after those. This is
        // fixtureInstanceSlot in run_conformance_test.go and moves with it.
        uint8_t *slot = zam_vm_static(&vm, 4);

        put("static.kind", zam_mark_word_of(slot)->kind);
        // Contained with no distance, which is the pair that says the slot is
        // storage the collector neither owns nor has to walk out of. Cleared, a
        // sweep would free memory malloc never returned.
        put("static.contained", zam_mark_word_of(slot)->contained);
        put("static.upvalue", zam_mark_word_of(slot)->upvalue);
        put("static.shortID", zam_object_header_of(slot)->shortID);
        put("static.elementCount", zam_object_header_of(slot)->elementCount);

        // What the header is for. The member is reached the way
        // memberHeaderBytes reaches one — the width off the value's own mark
        // word, then the offset the definition records — so a slot the compiler
        // left unstamped resolves this member a word early, and the word the
        // program wrote is then somewhere else entirely.
        const ZamMemberDefinition *member = zam_type_member(
            &vm.image, (ZamTypeShort)zam_object_header_of(slot)->shortID, 0);
        put("static.member",
            *(const ZamWord *)(slot +
                               zam_object_header_bytes(
                                   zam_mark_word_of(slot)->kind) +
                               member->offset));
    }

    if (wantMark && vm.main.markByteCount != 0) {
        // The topmost entry, found by walking the stack the way every reader of
        // it does: an entry is a fixed width per kind and they lie with nothing
        // between them, so the last header reached is the top. Walked rather
        // than measured back from the end, an entry's width not being knowable
        // from behind it.
        uint64_t last = 0;
        for (uint64_t at = 0; at < vm.main.markByteCount;) {
            last = at;
            at += zam_mark_entry_bytes(zam_mark_kind_at(&vm.main, at));
        }

       ZamMarkKind kind = zam_mark_kind_at(&vm.main, last);
        put("mark.kind", kind);

        // Which of the two it is decides how much there is to read, so the
        // narrower kind is not read as the wider one: an object entry is one
        // word and the two words an array entry carries past it would be
        // whatever lies above the stack.
        if (kind == ZAMMARK_ARRAY) {
           ZamMarkArray entry;
            memcpy(&entry, vm.main.marks + last, sizeof entry);
            put("mark.operand", entry.operand);
            put("mark.element", entry.element);
            put("mark.length", entry.length);
        } else if (kind == ZAMMARK_OBJECT) {
           ZamMarkObject entry;
            memcpy(&entry, vm.main.marks + last, sizeof entry);
            put("mark.operand", entry.operand);
        }
    }

    if (wantArray)
        putArray(&vm.image, (uint8_t *)(vm.main.operands + ZAMMAIN_FRAME_WORDS));

    if (wantMemberArray) {
        uint8_t *outer = (uint8_t *)(vm.main.operands + ZAMMAIN_FRAME_WORDS);
        const ZamMemberDefinition *member = zam_type_member(
            &vm.image, (ZamTypeShort)zam_object_header_of(outer)->shortID, 0);
        uint8_t *first = outer +
                         zam_object_header_bytes(zam_mark_word_of(outer)->kind) +
                         member->offset;

        // The front of the run, unlike the front of an allocation or of a value
        // standing on its own, does lie inside something — so its distance is
        // reported here, where it has one meaning, rather than in putArray,
        // where it would have three.
        put("member.upvalue", zam_mark_word_of(first)->upvalue);
        put("member.containerIsOuter", zam_container_of(first) == outer);
        putArray(&vm.image, first);
    }

    // The head of the sweep list, which for a program that allocated once is the
    // array. Reached from there rather than from the pointer the program left,
    // so that what is read is the block the allocator linked.
    if (wantHeapArray)
        putArray(&vm.image, (uint8_t *)vm.allocations);

    zam_vm_free(&vm);
    return 0;
}
