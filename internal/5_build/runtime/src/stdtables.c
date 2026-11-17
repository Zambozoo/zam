// The standard library's tables of natives beyond the core one, all of them,
// for a program run under the interpreter, which may call any. A natively
// compiled program defines its own, naming only the tables it calls into.

#include "zam/native.h"

extern const ZamNativeEntry zamJsonNativeTable[];
extern const uint64_t zamJsonNativeTableCount;
extern const ZamNativeEntry zamYamlNativeTable[];
extern const uint64_t zamYamlNativeTableCount;
extern const ZamNativeEntry zamXmlNativeTable[];
extern const uint64_t zamXmlNativeTableCount;
extern const ZamNativeEntry zamBsonNativeTable[];
extern const uint64_t zamBsonNativeTableCount;
extern const ZamNativeEntry zamCompressNativeTable[];
extern const uint64_t zamCompressNativeTableCount;
extern const ZamNativeEntry zamCborNativeTable[];
extern const uint64_t zamCborNativeTableCount;
extern const ZamNativeEntry zamArchiveNativeTable[];
extern const uint64_t zamArchiveNativeTableCount;
extern const ZamNativeEntry zamNetNativeTable[];
extern const uint64_t zamNetNativeTableCount;
extern const ZamNativeEntry zamTlsNativeTable[];
extern const uint64_t zamTlsNativeTableCount;
extern const ZamNativeEntry zamCurlNativeTable[];
extern const uint64_t zamCurlNativeTableCount;
extern const ZamNativeEntry zamHttpNativeTable[];
extern const uint64_t zamHttpNativeTableCount;
extern const ZamNativeEntry zamWsNativeTable[];
extern const uint64_t zamWsNativeTableCount;
extern const ZamNativeEntry zamCryptoNativeTable[];
extern const uint64_t zamCryptoNativeTableCount;
extern const ZamNativeEntry zamPkiNativeTable[];
extern const uint64_t zamPkiNativeTableCount;
extern const ZamNativeEntry zamTimeNativeTable[];
extern const uint64_t zamTimeNativeTableCount;
extern const ZamNativeEntry zamArenaNativeTable[];
extern const uint64_t zamArenaNativeTableCount;

const ZamNativeTables zamStdNativeTables[] = {
    {zamJsonNativeTable, &zamJsonNativeTableCount},
    {zamYamlNativeTable, &zamYamlNativeTableCount},
    {zamXmlNativeTable, &zamXmlNativeTableCount},
    {zamBsonNativeTable, &zamBsonNativeTableCount},
    {zamCompressNativeTable, &zamCompressNativeTableCount},
    {zamCborNativeTable, &zamCborNativeTableCount},
    {zamArchiveNativeTable, &zamArchiveNativeTableCount},
    {zamNetNativeTable, &zamNetNativeTableCount},
    {zamTlsNativeTable, &zamTlsNativeTableCount},
    {zamCurlNativeTable, &zamCurlNativeTableCount},
    {zamHttpNativeTable, &zamHttpNativeTableCount},
    {zamWsNativeTable, &zamWsNativeTableCount},
    {zamCryptoNativeTable, &zamCryptoNativeTableCount},
    {zamPkiNativeTable, &zamPkiNativeTableCount},
    {zamTimeNativeTable, &zamTimeNativeTableCount},
    {zamArenaNativeTable, &zamArenaNativeTableCount},
};
const uint64_t zamStdNativeTableCount = sizeof zamStdNativeTables / sizeof zamStdNativeTables[0];
