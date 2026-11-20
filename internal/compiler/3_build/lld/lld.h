#pragma once

#ifdef __cplusplus
extern "C" {
#endif

// zam_lld_link runs lld over argc arguments, argv[0] naming the flavor as lld's
// own executables do. It answers lld's exit code, leaves what lld wrote in a
// malloc'd *output, and clears *reusable when lld cannot safely run again in
// this process.
int zam_lld_link(int argc, const char **argv, char **output, int *reusable);

#ifdef __cplusplus
}
#endif
