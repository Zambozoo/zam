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

   ZamResult result = zam_vm_run(&vm, argc - 2, argv + 2);
    uint8_t status = vm.main.exitStatus;
    zam_vm_free(&vm);

    // Only a program that exited says what to end with. Anything else stopped
    // in a way the program did not choose, and reporting its status as the
    // program's would be claiming it ran.
    if (result != ZAMRESULT_EXIT)
        zam_die(ZAMEXIT_PANIC, "the program stopped: %s\n",
                zam_result_name(result));
    return status;
}
