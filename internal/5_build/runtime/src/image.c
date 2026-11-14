#include "image.h"

// The magic is compared as the word it was written as rather than by memcmp,
// which is the same eight bytes: the compiler assembles it little-endian for
// exactly this reason, so that a reader comparing one word and a person reading
// the file with `head` are looking at the same thing.
static ZamWord magicWord(void) {
   ZamWord word;
    memcpy(&word, "ZAM_LANG", ZAMWORD_BYTES);
    return word;
}

static void readFile(ZamImage *image, const char *path) {
    FILE *file = fopen(path, "rb");
    if (file == NULL)
        zam_die(ZAMEXIT_IMAGE, "cannot open image %s\n", path);

    if (fseek(file, 0, SEEK_END) != 0)
        zam_die(ZAMEXIT_IMAGE, "cannot size image %s\n", path);
    long byteCount = ftell(file);
    if (byteCount < 0)
        zam_die(ZAMEXIT_IMAGE, "cannot size image %s\n", path);
    rewind(file);

    image->byteCount = (size_t)byteCount;
    image->bytes = zam_alloc(image->byteCount);
    image->ownsBytes = true;
    if (fread(image->bytes, 1, image->byteCount, file) != image->byteCount)
        zam_die(ZAMEXIT_IMAGE, "cannot read image %s\n", path);
    fclose(file);
}

// section bounds every offset the header carries, so that a truncated or
// corrupt image is caught here rather than as a wild read somewhere in the
// dispatch loop. The offsets are all body-relative and the body is what remains
// of the file after the header.
static const uint8_t *section(const ZamImage *image, ZamHeaderWord word,
                              const char *name) {
   ZamWord offset = image->header[word];
    size_t bodyByteCount = image->byteCount - ZAMHEADER_WORDS * ZAMWORD_BYTES;
    if (offset > bodyByteCount)
        zam_die(ZAMEXIT_IMAGE,
                "image is truncated: %s begins at %llu, past the %zu bytes "
                "that follow the header\n",
                name, (unsigned long long)offset, bodyByteCount);
    return image->body + offset;
}

// debugWords reads count words at *cursor, dying if they run past end.
static const ZamWord *debugWords(const uint8_t **cursor, const uint8_t *end,
                                 uint64_t count, const char *label) {
    if (count > (uint64_t)(end - *cursor) / ZAMWORD_BYTES)
        zam_die(ZAMEXIT_IMAGE, "%s has a debug section past its end\n", label);
    const ZamWord *words = (const ZamWord *)*cursor;
    *cursor += count * ZAMWORD_BYTES;
    return words;
}

// parseDebug reads the debug section, which runs from debug to the end of
// the file: the strings and the function table, each opening with how many
// entries follow, then the line programs, opening with how many bytes they
// take.
static void parseDebug(ZamImage *image, const uint8_t *debug,
                       const char *label) {
    const uint8_t *end = image->bytes + image->byteCount;
    const uint8_t *cursor = debug;

    image->debugStringCount = *debugWords(&cursor, end, 1, label);
    if (image->debugStringCount > (uint64_t)(end - cursor) / ZAMWORD_BYTES)
        zam_die(ZAMEXIT_IMAGE, "%s has a debug section past its end\n", label);
    image->debugStrings = zam_alloc((image->debugStringCount + 1) * sizeof(char *));
    for (uint64_t i = 0; i < image->debugStringCount; i++) {
        ZamWord length = *debugWords(&cursor, end, 1, label);
        ZamWord padded = (length + 1 + ZAMWORD_BYTES - 1) / ZAMWORD_BYTES;
        image->debugStrings[i] = (const char *)debugWords(&cursor, end, padded, label);
    }

    image->debugFunctionCount = *debugWords(&cursor, end, 1, label);
    if (image->debugFunctionCount > (uint64_t)(end - cursor) / ZAMWORD_BYTES / ZAMDEBUG_FUNCTION_WORDS)
        zam_die(ZAMEXIT_IMAGE, "%s has a debug section past its end\n", label);
    image->debugFunctions = debugWords(
        &cursor, end, image->debugFunctionCount * ZAMDEBUG_FUNCTION_WORDS, label);

    image->debugProgramsByteCount = *debugWords(&cursor, end, 1, label);
    if (image->debugProgramsByteCount > (uint64_t)(end - cursor))
        zam_die(ZAMEXIT_IMAGE, "%s has a debug section past its end\n", label);
    image->debugPrograms = cursor;
}

// lastStartingBy answers the index of the last of count entries, each words
// wide and opening with where it starts, that starts at or before offset, or
// count if none does.
static uint64_t lastStartingBy(const ZamWord *entries, uint64_t count,
                               uint64_t words, uint64_t offset) {
    uint64_t low = 0, high = count;
    while (low < high) {
        uint64_t middle = low + (high - low) / 2;
        if (entries[middle * words] <= offset)
            low = middle + 1;
        else
            high = middle;
    }
    return low == 0 ? count : low - 1;
}

// innermostFunction answers the function table entry of the innermost
// function whose body holds the instruction at offset, or NULL.
static const ZamWord *innermostFunction(const ZamImage *image, uint64_t offset) {
    uint64_t i = lastStartingBy(image->debugFunctions, image->debugFunctionCount,
                                ZAMDEBUG_FUNCTION_WORDS, offset);
    // A function starting at or before the offset may have ended before it,
    // the offset lying past a body nested in the one that holds it, so the
    // enclosing functions are tried outward until one holds it.
    while (i < image->debugFunctionCount) {
        const ZamWord *function = image->debugFunctions + i * ZAMDEBUG_FUNCTION_WORDS;
        if (offset < function[1])
            return function;
        // A parent lies ahead of what it encloses, which is also what keeps a
        // corrupt table from sending the walk round in a circle.
        i = function[2] < i ? function[2] : image->debugFunctionCount;
    }
    return NULL;
}

const char *zam_image_function(const ZamImage *image, uint64_t offset) {
    const ZamWord *function = innermostFunction(image, offset);
    if (function == NULL || function[3] >= image->debugStringCount)
        return NULL;
    return image->debugStrings[function[3]];
}

// uvarint reads an unsigned varint at *cursor, answering false if it runs past
// end.
static bool uvarint(const uint8_t **cursor, const uint8_t *end, uint64_t *value) {
    *value = 0;
    for (unsigned shift = 0; *cursor < end && shift < 64; shift += 7) {
        uint8_t byte = *(*cursor)++;
        *value |= (uint64_t)(byte & 0x7f) << shift;
        if ((byte & 0x80) == 0)
            return true;
    }
    return false;
}

// A function's line program opens with how many lines it says. Each is then
// how far its start lies past the one before it, shifted up a bit that says
// the file changes; how far its line lies from the one before it, zigzag
// encoded; and the new file's index if it changed. The first is measured from
// where the function starts and from line zero.
bool zam_image_locate(const ZamImage *image, uint64_t offset, const char **file,
                      uint32_t *line) {
    const ZamWord *function = innermostFunction(image, offset);
    if (function == NULL || function[4] >= image->debugProgramsByteCount)
        return false;
    const uint8_t *cursor = image->debugPrograms + function[4];
    const uint8_t *end = image->debugPrograms + image->debugProgramsByteCount;

    uint64_t count, start = function[0], fileIndex = UINT64_MAX;
    int64_t at = 0;
    bool found = false;
    if (!uvarint(&cursor, end, &count))
        return false;
    for (uint64_t i = 0; i < count; i++) {
        uint64_t step, zigzag;
        if (!uvarint(&cursor, end, &step) || !uvarint(&cursor, end, &zigzag))
            return false;
        uint64_t next = start + (step >> 1);
        if (next > offset)
            break;
        start = next;
        at += (int64_t)(zigzag >> 1) ^ -(int64_t)(zigzag & 1);
        if ((step & 1) != 0 && !uvarint(&cursor, end, &fileIndex))
            return false;
        found = true;
    }
    if (!found || fileIndex >= image->debugStringCount)
        return false;
    *file = image->debugStrings[fileIndex];
    *line = (uint32_t)at;
    return true;
}

// parseAndValidate reads the header off image->bytes and fills in everything
// after it. It is the half of load that is the same whether the bytes came
// from a file or from a byte array baked into the binary, and is written to
// read a label of "image" when the source is anonymous.
static void parseAndValidate(ZamImage *image, const char *label) {
    size_t headerByteCount = ZAMHEADER_WORDS * ZAMWORD_BYTES;
    if (image->byteCount < headerByteCount)
        zam_die(ZAMEXIT_IMAGE, "%s is %zu bytes, too short to be an image\n",
                label, image->byteCount);

    image->header = (const ZamWord *)image->bytes;
    image->body = image->bytes + headerByteCount;

    if (image->header[ZAMHEADER_MAGIC] != magicWord())
        zam_die(ZAMEXIT_IMAGE, "%s is not a Zamylene image\n", label);
    if (image->header[ZAMHEADER_VERSION] != ZAMVERSION_WORD) {
       ZamWord version = image->header[ZAMHEADER_VERSION];
        zam_die(ZAMEXIT_IMAGE,
                "%s is image version %llu.%llu.%llu; this runtime reads "
                "%d.%d.%d\n",
                label, (unsigned long long)(version >> 32),
                (unsigned long long)((version >> 16) & 0xffff),
                (unsigned long long)(version & 0xffff), ZAMVERSION_MAJOR,
                ZAMVERSION_MINOR, ZAMVERSION_PATCH);
    }

    // The body opens with the ten words of runtime configuration, so the type
    // definition map begins after them.
    const ZamWord *configuration = (const ZamWord *)image->body;
    memcpy(&image->gcTriggerRatio, &configuration[0], sizeof(double));
    image->initialStackSize = configuration[1];
    image->initialHeapSize = configuration[2];
    image->nurserySize = configuration[3];
    image->maxHeapSize = configuration[4];
    image->gcMarkers = configuration[5];
    image->gcConcurrent = configuration[6] != 0;
    memcpy(&image->gcRemarkGrowth, &configuration[7], sizeof(double));
    image->gcPrecleanPasses = configuration[8];
    image->gcReturnAge = configuration[9];

    image->typeDefinitionCount = image->header[ZAMHEADER_TYPE_DEFINITION_COUNT];
    image->typeDefinitionMap = &configuration[10];

    image->staticSlots =
        (const ZamWord *)section(image, ZAMHEADER_STATIC_SLOTS_OFFSET,
                                 "the static slot map");
    image->staticRegionByteCount = image->header[ZAMHEADER_STATIC_REGION_BYTES];

    image->staticImage = section(image, ZAMHEADER_STATIC_IMAGE_OFFSET,
                                 "the static region's initial bytes");
    image->natives = section(image, ZAMHEADER_NATIVES_OFFSET, "the native table");
    image->genericStaticMethods = section(image,
                                          ZAMHEADER_GENERIC_STATIC_METHODS_OFFSET,
                                          "the generic static method registry");
    image->staticRoots = (const ZamWord *)section(
        image, ZAMHEADER_STATIC_ROOTS_OFFSET, "the static root table");
    image->operations =
        section(image, ZAMHEADER_OPERATIONS_OFFSET, "the operations");
    const uint8_t *debug =
        section(image, ZAMHEADER_DEBUG_OFFSET, "the debug section");

    // Three of the sections are told no length of their own and are bounded by
    // whatever begins next, the last of them by the end of the file. The
    // compiler lays them out in this order and takes care that operations come
    // last, which is what makes the file's own end the program's.
    if (image->staticImage < (const uint8_t *)image->staticSlots ||
        image->natives < image->staticImage ||
        image->genericStaticMethods < image->natives ||
        (const uint8_t *)image->staticRoots < image->genericStaticMethods ||
        image->operations < (const uint8_t *)image->staticRoots ||
        debug < image->operations)
        zam_die(ZAMEXIT_IMAGE, "%s has sections out of order\n", label);

    image->staticSlotCount =
        (size_t)((const ZamWord *)image->staticImage - image->staticSlots);
    image->staticImageByteCount =
        (size_t)(image->natives - image->staticImage);
    image->operationsByteCount = (size_t)(debug - image->operations);

    // The native table is the one section that says its own length, because it
    // is a run of names that are not all the same width: a reader cannot find
    // the end of it by walking, only by being told how many to walk.
    image->nativeCount = *(const ZamWord *)image->natives;

    // The generic static method registry opens with its own entry count the
    // same way: the entries are not fixed width — arity, concrete count, then
    // a run of concretes each as wide as the arity makes it — so the walk is
    // length-bounded rather than offset-bounded.
    image->genericStaticMethodCount = *(const ZamWord *)image->genericStaticMethods;

    image->staticRootCount = image->staticRoots[0];
    if ((const uint8_t *)(image->staticRoots + 1 +
                          image->staticRootCount * ZAMSTATIC_ROOT_WORDS) >
        image->operations)
        zam_die(ZAMEXIT_IMAGE, "%s has a static root table past its end\n",
                label);

    parseDebug(image, debug, label);
}

uint64_t zam_image_static_root(const ZamImage *image, uint64_t index,
                               ZamTypeID *type) {
    const ZamWord *entry =
        image->staticRoots + 1 + index * ZAMSTATIC_ROOT_WORDS;
    *type = zam_type_id_read(entry + 1);
    return entry[0];
}

void zam_image_open(ZamImage *image, const char *path) {
    *image = (ZamImage){0};
    readFile(image, path);
    parseAndValidate(image, path);
}

void zam_image_load(ZamImage *image, const uint8_t *bytes, size_t byteCount) {
    *image = (ZamImage){0};
    // The bytes are read in place and are the caller's: the runtime never
    // writes through them, and will not free them on close. A pointer into
    // read-only static storage from the embedded image is the usual case.
    image->bytes = (uint8_t *)bytes;
    image->byteCount = byteCount;
    image->ownsBytes = false;
    parseAndValidate(image, "image");
}

void zam_image_close(ZamImage *image) {
    if (image->ownsBytes)
        free(image->bytes);
    free(image->debugStrings);
    *image = (ZamImage){0};
}

ZamTypeBucket zam_image_bucket_of(const ZamImage *image, uint16_t declID) {
    if (declID < ZAMPRIMITIVE_COUNT)
        return ZAMBUCKET_PRIMITIVE;

    // The buckets are contiguous and in header order, so the last start index
    // the id is at or above is the bucket it belongs to.
    static const ZamHeaderWord starts[] = {
        ZAMHEADER_THREAD_TYPES_START,       ZAMHEADER_FUNCTION_TYPES_START,
        ZAMHEADER_POINTERS_START,           ZAMHEADER_TUPLES_START,
        ZAMHEADER_STRUCTS_START,            ZAMHEADER_GENERIC_STRUCTS_START,
        ZAMHEADER_ENUMS_START,              ZAMHEADER_CLASSES_START,
        ZAMHEADER_GENERIC_CLASSES_START,    ZAMHEADER_ABSTRACTS_START,
        ZAMHEADER_GENERIC_ABSTRACTS_START,  ZAMHEADER_INTERFACES_START,
        ZAMHEADER_GENERIC_INTERFACES_START,
    };

   ZamTypeBucket bucket = ZAMBUCKET_PRIMITIVE;
    for (size_t i = 0; i < sizeof(starts) / sizeof(starts[0]); i++)
        if (declID >= image->header[starts[i]])
            bucket = (ZamTypeBucket)(ZAMBUCKET_THREAD_TYPE + i);
    return bucket;
}

const uint8_t *zam_image_type_definition(const ZamImage *image,
                                         uint16_t declID) {
    if (declID >= image->typeDefinitionCount)
        zam_die(ZAMEXIT_PANIC, "type id %u is past the %llu the image defines\n",
                declID, (unsigned long long)image->typeDefinitionCount);
    if (declID < ZAMPRIMITIVE_COUNT)
        zam_die(ZAMEXIT_PANIC, "type id %u is a primitive and has no definition\n",
                declID);
    return image->body + image->typeDefinitionMap[declID];
}

uint64_t zam_image_static_offset(const ZamImage *image, uint64_t slot) {
    if (slot >= image->staticSlotCount)
        zam_die(ZAMEXIT_PANIC, "static slot %llu is past the %llu the image has\n",
                (unsigned long long)slot,
                (unsigned long long)image->staticSlotCount);
    return image->staticSlots[slot];
}

// genericStaticMethodConcreteBytes is how wide one concrete row is: the type
// arguments laid as three-word typeInstances, then the slot, then the function
// type id. Kept in step with the writer in codegen's readGenericStaticMethods.
static size_t genericStaticMethodConcreteBytes(uint64_t arity) {
    return (arity * ZAMTYPE_ID_WORDS + 1 + ZAMTYPE_ID_WORDS) * ZAMWORD_BYTES;
}

const uint8_t *zam_image_generic_static_method_entry(const ZamImage *image,
                                                     uint64_t id,
                                                     uint64_t *arity,
                                                     uint64_t *concreteCount) {
    if (id >= image->genericStaticMethodCount)
        zam_die(ZAMEXIT_PANIC,
                "generic static method id %llu is past the %llu the image "
                "has\n",
                (unsigned long long)id,
                (unsigned long long)image->genericStaticMethodCount);

    // Entries are variable-width, so the nth is reached by walking the ones
    // before it. The walk is linear in the id, which is bearable: there are
    // usually only a handful, and a program names each at most once per call
    // site. Caching this would be a load-time pass over the whole table.
    const uint8_t *cursor = image->genericStaticMethods + ZAMWORD_BYTES;
    for (uint64_t i = 0; i < id; i++) {
        uint64_t entryArity = ((const ZamWord *)cursor)[0];
        uint64_t entryConcretes = ((const ZamWord *)cursor)[1];
        cursor += 2 * ZAMWORD_BYTES
                  + entryConcretes * genericStaticMethodConcreteBytes(entryArity);
    }
    *arity = ((const ZamWord *)cursor)[0];
    *concreteCount = ((const ZamWord *)cursor)[1];
    return cursor + 2 * ZAMWORD_BYTES;
}

const char *zam_image_native_name(const ZamImage *image, uint64_t slot,
                                  uint64_t *byteCount) {
    if (slot >= image->nativeCount)
        zam_die(ZAMEXIT_PANIC, "native slot %llu is past the %llu the image names\n",
                (unsigned long long)slot, (unsigned long long)image->nativeCount);

    // The names are not all the same width, so the nth is reached by walking
    // the ones before it. Slots are resolved once at load, so this is a walk of
    // the table per program rather than per call.
    const uint8_t *cursor = image->natives + ZAMWORD_BYTES;
    for (uint64_t i = 0;; i++) {
        uint64_t length = *(const ZamWord *)cursor;
        const char *name = (const char *)(cursor + ZAMWORD_BYTES);
        if (i == slot) {
            *byteCount = length;
            return name;
        }
        // A name is padded out to a whole word the way a literal is — length,
        // then the bytes, then a NUL, then any pad. The +1 is that terminator
        // and matches stringLiteral.ByteCount on the compiler side; both have
        // to say the same thing or the walker lands between names.
        cursor += ZAMWORD_BYTES + (length + 1 + ZAMWORD_BYTES - 1) /
                                       ZAMWORD_BYTES * ZAMWORD_BYTES;
    }
}
