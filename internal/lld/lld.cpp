#include "lld.h"

#include <cstdlib>
#include <cstring>
#include <string>

#include "lld/Common/Driver.h"
#include "llvm/Support/raw_ostream.h"

LLD_HAS_DRIVER(coff)
LLD_HAS_DRIVER(elf)
LLD_HAS_DRIVER(macho)
LLD_HAS_DRIVER(mingw)

extern "C" int zam_lld_link(int argc, const char **argv, char **output, int *reusable) {
    std::string out;
    llvm::raw_string_ostream os(out);
    lld::Result result = lld::lldMain(llvm::ArrayRef<const char *>(argv, argc), os, os,
                                      {
                                          {lld::WinLink, &lld::coff::link},
                                          {lld::Gnu, &lld::elf::link},
                                          {lld::MinGW, &lld::mingw::link},
                                          {lld::Darwin, &lld::macho::link},
                                      });
    os.flush();
    *output = static_cast<char *>(std::malloc(out.size() + 1));
    if (*output != nullptr)
        std::memcpy(*output, out.c_str(), out.size() + 1);
    *reusable = result.canRunAgain;
    return result.retCode;
}
