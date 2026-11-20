// readImage, shared by the harnesses that run a VM over an image file.

#ifndef ZAM_HARNESS_IMAGE_H
#define ZAM_HARNESS_IMAGE_H

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

// readImage answers the bytes of the image file at path, setting byteCount, and
// exits when it cannot be read.
static uint8_t *readImage(const char *path, size_t *byteCount) {
    FILE *file = fopen(path, "rb");
    if (file == NULL || fseek(file, 0, SEEK_END) != 0) {
        fprintf(stderr, "cannot open image %s\n", path);
        exit(2);
    }
    long size = ftell(file);
    rewind(file);
    uint8_t *bytes = malloc(size > 0 ? (size_t)size : 1);
    if (size < 0 || bytes == NULL || fread(bytes, 1, (size_t)size, file) != (size_t)size) {
        fprintf(stderr, "cannot read image %s\n", path);
        exit(2);
    }
    fclose(file);
    *byteCount = (size_t)size;
    return bytes;
}

#endif
