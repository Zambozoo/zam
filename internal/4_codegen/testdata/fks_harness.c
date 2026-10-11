/*
 * Test harness around fks.c. It exists only so the Go test can drive the
 * reference lookup; the runtime copies fks.c, not this.
 *
 * fks.c is included rather than linked so the harness can reach the pieces the
 * lookup is built from. Resolving keys is not enough to pin them all down: an
 * index is read from the high bits of a product, so a constant that is wrong in
 * its low bits still answers most tables correctly. The pieces have to be asked
 * about directly, at ranges where a small difference has somewhere to show.
 *
 *   lookup <table>  reads a decimal key per line and prints the value it
 *                   resolves to.
 *   index           reads "key seed n" per line and prints the hash and the
 *                   index each comes to.
 */

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

#include "fks.c"

static int lookup_mode(const char *path) {
    FILE *file = fopen(path, "rb");
    if (file == NULL) {
        perror(path);
        return 2;
    }
    if (fseek(file, 0, SEEK_END) != 0) {
        return 2;
    }
    long size = ftell(file);
    if (size < 0 || fseek(file, 0, SEEK_SET) != 0) {
        return 2;
    }

    unsigned char *table = malloc((size_t)size);
    if (table == NULL || fread(table, 1, (size_t)size, file) != (size_t)size) {
        fprintf(stderr, "could not read %s\n", path);
        return 2;
    }
    fclose(file);

    unsigned long long key;
    while (scanf("%llu", &key) == 1) {
        printf("%u\n", fks_lookup(table, (uint64_t)key));
    }

    free(table);
    return 0;
}

static int index_mode(void) {
    unsigned long long key, seed;
    unsigned long n;
    while (scanf("%llu %llu %lu", &key, &seed, &n) == 3) {
        printf("%llu %u\n",
               (unsigned long long)fks_hash((uint64_t)key, (uint64_t)seed),
               fks_index((uint64_t)key, (uint64_t)seed, (uint32_t)n));
    }
    return 0;
}

int main(int argc, char **argv) {
    if (argc == 3 && argv[1][0] == 'l') {
        return lookup_mode(argv[2]);
    }
    if (argc == 2 && argv[1][0] == 'i') {
        return index_mode();
    }
    fprintf(stderr, "usage: %s lookup <table> | %s index\n", argv[0], argv[0]);
    return 2;
}
