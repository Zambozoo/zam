#include <stdio.h>
#include <sys/types.h>
#include <time.h>

#include "codec.h"

// The archive natives, over libarchive. macOS ships the library but not its
// header, so the part of its API used here is declared below; every
// declaration has been stable since libarchive 3.0.
//
// An archive is read whole into a list of entries the zam side copies out
// and frees, and written entry by entry into a buffer it takes at the end.

struct archive;
struct archive_entry;

#define ARCHIVE_EOF 1
#define ARCHIVE_OK 0
#define ARCHIVE_WARN (-20)
#define AE_IFREG 0100000
#define AE_IFDIR 0040000

typedef ssize_t archive_write_callback(struct archive *, void *, const void *, size_t);
typedef int archive_open_callback(struct archive *, void *);
typedef int archive_close_callback(struct archive *, void *);

struct archive *archive_read_new(void);
int archive_read_support_filter_all(struct archive *);
int archive_read_support_format_tar(struct archive *);
int archive_read_support_format_zip(struct archive *);
int archive_read_open_memory(struct archive *, const void *, size_t);
int archive_read_next_header(struct archive *, struct archive_entry **);
ssize_t archive_read_data(struct archive *, void *, size_t);
int archive_read_free(struct archive *);
const char *archive_error_string(struct archive *);

struct archive *archive_write_new(void);
int archive_write_set_format_pax_restricted(struct archive *);
int archive_write_set_format_zip(struct archive *);
int archive_write_set_bytes_in_last_block(struct archive *, int);
int archive_write_open(struct archive *, void *, archive_open_callback *,
                       archive_write_callback *, archive_close_callback *);
int archive_write_header(struct archive *, struct archive_entry *);
ssize_t archive_write_data(struct archive *, const void *, size_t);
int archive_write_close(struct archive *);
int archive_write_free(struct archive *);

struct archive_entry *archive_entry_new(void);
void archive_entry_free(struct archive_entry *);
const char *archive_entry_pathname_utf8(struct archive_entry *);
int64_t archive_entry_size(struct archive_entry *);
mode_t archive_entry_filetype(struct archive_entry *);
mode_t archive_entry_perm(struct archive_entry *);
time_t archive_entry_mtime(struct archive_entry *);
void archive_entry_set_pathname_utf8(struct archive_entry *, const char *);
void archive_entry_set_size(struct archive_entry *, int64_t);
void archive_entry_set_filetype(struct archive_entry *, unsigned int);
void archive_entry_set_perm(struct archive_entry *, mode_t);
void archive_entry_set_mtime(struct archive_entry *, time_t, long);

// The formats the zam side numbers.
enum { FORMAT_TAR, FORMAT_ZIP };

typedef struct ArchiveEntry {
    char *name;
    int64_t mode;
    int64_t modTime;
    bool isDir;
    CodecBuffer data;
} ArchiveEntry;

typedef struct ArchiveList {
    size_t count;
    ArchiveEntry *entries;
} ArchiveList;

static void freeList(ArchiveList *list) {
    for (size_t i = 0; i < list->count; i++) {
        free(list->entries[i].name);
        free(list->entries[i].data.bytes);
    }
    free(list->entries);
    free(list);
}

// stdArchiveRead reads every entry of an archive in memory. A tar may be
// compressed by any filter libarchive knows, which it detects.
static void stdArchiveRead(ZamThread *thread) {
    ZamInt n = (ZamInt)zam_native_argument(thread, 1);
    const void *data = n == 0 ? "" : zam_native_argument_pointer(thread, 0);
    int format = (int)zam_native_argument(thread, 2);

    struct archive *a = archive_read_new();
    if (a == NULL)
        zam_native_panic(thread, "out of memory reading an archive");
    archive_read_support_filter_all(a);
    if (format == FORMAT_TAR)
        archive_read_support_format_tar(a);
    else
        archive_read_support_format_zip(a);

    ArchiveList *list = calloc(1, sizeof *list);
    if (list == NULL)
        abort();
    size_t capacity = 0;
    char message[256] = "";
    int result = archive_read_open_memory(a, data, (size_t)n);
    struct archive_entry *header;
    while (result >= ARCHIVE_WARN) {
        result = archive_read_next_header(a, &header);
        if (result == ARCHIVE_EOF || result < ARCHIVE_WARN)
            break;
        if (list->count == capacity) {
            capacity = capacity == 0 ? 8 : capacity * 2;
            ArchiveEntry *grown = realloc(list->entries, capacity * sizeof *grown);
            if (grown == NULL)
                abort();
            list->entries = grown;
        }
        ArchiveEntry *entry = &list->entries[list->count++];
        memset(entry, 0, sizeof *entry);
        const char *name = archive_entry_pathname_utf8(header);
        entry->name = strdup(name == NULL ? "" : name);
        entry->mode = archive_entry_perm(header) & 07777;
        entry->modTime = (int64_t)archive_entry_mtime(header);
        entry->isDir = (archive_entry_filetype(header) & 0170000) == AE_IFDIR;
        char chunk[16384];
        ssize_t read;
        while ((read = archive_read_data(a, chunk, sizeof chunk)) > 0)
            codec_buffer_append(&entry->data, chunk, (size_t)read);
        if (read < 0) {
            result = (int)read;
            break;
        }
    }
    if (result != ARCHIVE_EOF) {
        const char *err = archive_error_string(a);
        snprintf(message, sizeof message, "%s", err != NULL ? err : "invalid archive");
    }
    archive_read_free(a);

    if (message[0] != '\0') {
        freeList(list);
        codec_write_word(thread, 3, 0);
        *(ZamInt *)zam_native_argument_pointer(thread, 4) = 0;
        codec_write_error(thread, 5, message);
    } else {
        codec_write_word(thread, 3, (ZamWord)(uintptr_t)list);
        *(ZamInt *)zam_native_argument_pointer(thread, 4) = (ZamInt)list->count;
        codec_write_error(thread, 5, NULL);
    }
    zam_native_return_void(thread);
}

static ArchiveEntry *entryArgument(ZamThread *thread) {
    ArchiveList *list = codec_handle(thread, 0);
    ZamInt i = (ZamInt)zam_native_argument(thread, 1);
    if (list == NULL || i < 0 || (size_t)i >= list->count)
        zam_native_panic(thread, "archive entry index out of range");
    return &list->entries[i];
}

// stdArchiveEntry answers the fields of entry i of a read list.
static void stdArchiveEntry(ZamThread *thread) {
    ArchiveEntry *entry = entryArgument(thread);
    codec_write_string(thread, 2, entry->name, strlen(entry->name));
    *(ZamInt *)zam_native_argument_pointer(thread, 3) = entry->mode;
    *(ZamInt *)zam_native_argument_pointer(thread, 4) = entry->modTime;
    codec_write_bool(thread, 5, entry->isDir);
    *(ZamInt *)zam_native_argument_pointer(thread, 6) = (ZamInt)entry->data.length;
    zam_native_return_void(thread);
}

static void stdArchiveEntryData(ZamThread *thread) {
    ArchiveEntry *entry = entryArgument(thread);
    if (entry->data.length > 0)
        memcpy(zam_native_argument_pointer(thread, 2), entry->data.bytes, entry->data.length);
    zam_native_return_void(thread);
}

static void stdArchiveFree(ZamThread *thread) {
    ArchiveList *list = codec_handle(thread, 0);
    if (list != NULL)
        freeList(list);
    zam_native_return_void(thread);
}

typedef struct ArchiveWriter {
    struct archive *a;
    CodecBuffer out;
} ArchiveWriter;

static ssize_t writeOut(struct archive *a, void *client, const void *buffer, size_t length) {
    (void)a;
    codec_buffer_append(&((ArchiveWriter *)client)->out, buffer, length);
    return (ssize_t)length;
}

static void stdArchiveWriterNew(ZamThread *thread) {
    ArchiveWriter *w = calloc(1, sizeof *w);
    if (w == NULL || (w->a = archive_write_new()) == NULL)
        zam_native_panic(thread, "out of memory writing an archive");
    if (zam_native_argument(thread, 0) == FORMAT_TAR) {
        archive_write_set_format_pax_restricted(w->a);
        archive_write_set_bytes_in_last_block(w->a, 1);
    } else {
        archive_write_set_format_zip(w->a);
    }
    if (archive_write_open(w->a, w, NULL, writeOut, NULL) != ARCHIVE_OK)
        zam_native_panic(thread, archive_error_string(w->a));
    codec_write_word(thread, 1, (ZamWord)(uintptr_t)w);
    zam_native_return_void(thread);
}

// stdArchiveWriterAdd writes one entry: a file holding n bytes, or a
// directory holding none.
static void stdArchiveWriterAdd(ZamThread *thread) {
    ArchiveWriter *w = codec_handle(thread, 0);
    char *name = codec_c_string(thread, 1);
    ZamInt n = (ZamInt)zam_native_argument(thread, 3);
    const void *data = n == 0 ? "" : zam_native_argument_pointer(thread, 2);
    bool dir = zam_native_argument(thread, 6) != 0;

    struct archive_entry *entry = archive_entry_new();
    archive_entry_set_pathname_utf8(entry, name);
    archive_entry_set_filetype(entry, dir ? AE_IFDIR : AE_IFREG);
    archive_entry_set_perm(entry, (mode_t)zam_native_argument(thread, 4));
    archive_entry_set_mtime(entry, (time_t)(ZamInt)zam_native_argument(thread, 5), 0);
    archive_entry_set_size(entry, dir ? 0 : n);
    free(name);

    const char *err = NULL;
    if (archive_write_header(w->a, entry) < ARCHIVE_WARN)
        err = archive_error_string(w->a);
    else if (!dir && n > 0 && archive_write_data(w->a, data, (size_t)n) < 0)
        err = archive_error_string(w->a);
    archive_entry_free(entry);
    codec_write_error(thread, 7, err == NULL ? NULL : err);
    zam_native_return_void(thread);
}

// stdArchiveWriterFinish closes the archive and leaves its bytes in a buffer
// for STD_CODEC_TAKE.
static void stdArchiveWriterFinish(ZamThread *thread) {
    ArchiveWriter *w = codec_handle(thread, 0);
    char message[256] = "";
    if (archive_write_close(w->a) != ARCHIVE_OK) {
        const char *err = archive_error_string(w->a);
        snprintf(message, sizeof message, "%s", err != NULL ? err : "archive did not close");
    }
    archive_write_free(w->a);
    if (message[0] != '\0') {
        free(w->out.bytes);
        free(w);
        codec_write_word(thread, 1, 0);
        *(ZamInt *)zam_native_argument_pointer(thread, 2) = 0;
        codec_write_error(thread, 3, message);
    } else {
        CodecBuffer *b = malloc(sizeof *b);
        if (b == NULL)
            abort();
        *b = w->out;
        free(w);
        codec_write_word(thread, 1, (ZamWord)(uintptr_t)b);
        *(ZamInt *)zam_native_argument_pointer(thread, 2) = (ZamInt)b->length;
        codec_write_error(thread, 3, NULL);
    }
    zam_native_return_void(thread);
}

const ZamNativeEntry zamArchiveNativeTable[] = {
    {"STD_ARCHIVE_READ", stdArchiveRead},
    {"STD_ARCHIVE_ENTRY", stdArchiveEntry},
    {"STD_ARCHIVE_ENTRY_DATA", stdArchiveEntryData},
    {"STD_ARCHIVE_FREE", stdArchiveFree},
    {"STD_ARCHIVE_WRITER_NEW", stdArchiveWriterNew},
    {"STD_ARCHIVE_WRITER_ADD", stdArchiveWriterAdd},
    {"STD_ARCHIVE_WRITER_FINISH", stdArchiveWriterFinish},
};

const uint64_t zamArchiveNativeTableCount =
    sizeof(zamArchiveNativeTable) / sizeof(zamArchiveNativeTable[0]);
