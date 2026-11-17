#pragma once

// bcrypt password hashing, vendored from OpenBSD's libc.

#include <stddef.h>

// BCRYPT_HASH_SIZE is how many bytes a hash takes, its NUL included.
#define BCRYPT_HASH_SIZE 61

// bcrypt_newhash writes the $2b$ hash of pass under a fresh salt at
// log_rounds, clamped to 4 through 31, to hash, answering 0 or -1 with errno
// set.
int bcrypt_newhash(const char *pass, int log_rounds, char *hash, size_t hashlen);

// bcrypt_checkpass answers 0 when pass matches goodhash, a $2a$, $2b$ or $2y$
// hash, and -1 with errno set otherwise.
int bcrypt_checkpass(const char *pass, const char *goodhash);
