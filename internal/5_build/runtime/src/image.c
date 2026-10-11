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

    // The body opens with the three words of runtime configuration, so the type
    // definition map begins after them.
    const ZamWord *configuration = (const ZamWord *)image->body;
    memcpy(&image->gcTriggerRatio, &configuration[0], sizeof(double));
    image->initialStackSize = configuration[1];
    image->initialHeapSize = configuration[2];

    image->typeDefinitionCount = image->header[ZAMHEADER_TYPE_DEFINITION_COUNT];
    image->typeDefinitionMap = &configuration[3];

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
    image->operations =
        section(image, ZAMHEADER_OPERATIONS_OFFSET, "the operations");

    // Three of the sections are told no length of their own and are bounded by
    // whatever begins next, the last of them by the end of the file. The
    // compiler lays them out in this order and takes care that operations come
    // last, which is what makes the file's own end the program's.
    if (image->staticImage < (const uint8_t *)image->staticSlots ||
        image->natives < image->staticImage ||
        image->genericStaticMethods < image->natives ||
        image->operations < image->genericStaticMethods)
        zam_die(ZAMEXIT_IMAGE, "%s has sections out of order\n", label);

    image->staticSlotCount =
        (size_t)((const ZamWord *)image->staticImage - image->staticSlots);
    image->staticImageByteCount =
        (size_t)(image->natives - image->staticImage);
    image->operationsByteCount =
        image->byteCount - (size_t)(image->operations - image->bytes);

    // The native table is the one section that says its own length, because it
    // is a run of names that are not all the same width: a reader cannot find
    // the end of it by walking, only by being told how many to walk.
    image->nativeCount = *(const ZamWord *)image->natives;

    // The generic static method registry opens with its own entry count the
    // same way: the entries are not fixed width — arity, concrete count, then
    // a run of concretes each as wide as the arity makes it — so the walk is
    // length-bounded rather than offset-bounded.
    image->genericStaticMethodCount = *(const ZamWord *)image->genericStaticMethods;
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
    *image = (ZamImage){0};
}

ZamTypeBucket zam_image_bucket_of(const ZamImage *image, uint16_t declID) {
    if (declID < ZAMPRIMITIVE_COUNT)
        return ZAMBUCKET_PRIMITIVE;

    // The buckets are contiguous and in header order, so the last start index
    // the id is at or above is the bucket it belongs to.
    static const ZamHeaderWord starts[] = {
        ZAMHEADER_THREAD_TYPES_START,       ZAMHEADER_FUNCTION_TYPES_START,
        ZAMHEADER_POINTERS_START,           ZAMHEADER_STRUCTS_START,
        ZAMHEADER_GENERIC_STRUCTS_START,    ZAMHEADER_ENUMS_START,
        ZAMHEADER_CLASSES_START,            ZAMHEADER_GENERIC_CLASSES_START,
        ZAMHEADER_ABSTRACTS_START,          ZAMHEADER_GENERIC_ABSTRACTS_START,
        ZAMHEADER_INTERFACES_START,         ZAMHEADER_GENERIC_INTERFACES_START,
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
