#include "vm.h"

// The standalone runner is built from only the standard library's natives,
// since it loads an image chosen on the command line and knows nothing about
// which project wrote it. A project that wants its own natives is compiled
// with the build driver, which omits this translation unit and supplies its
// own definitions of these symbols beside its generated main.
const ZamNativeEntry zamProjectNativeTable[] = {{NULL, NULL}};
const uint64_t zamProjectNativeTableCount = 0;

int main(int argc, const char *argv[]) {
    if (argc < 2)
        zam_die(ZAMEXIT_USAGE, "usage: %s <image> [args...]\n", argv[0]);

    // The arguments past the image are the program's own, and reach it as
    // main's (int, str...) parameters. The runner's own argv[0] and the
    // image path at argv[1] are stripped here; everything after is the
    // program's.
   ZamVM vm;
    zam_vm_init(&vm, argv[1]);

    zam_vm_exit(&vm, zam_vm_run(&vm, argc - 2, argv + 2));
}
