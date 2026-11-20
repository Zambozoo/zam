// The part of SystemConfiguration the libraries in a macOS target pack call.
#ifndef ZAM_STUB_SCDYNAMICSTORECOPYSPECIFIC_H
#define ZAM_STUB_SCDYNAMICSTORECOPYSPECIFIC_H

#include <CoreFoundation/CoreFoundation.h>

typedef const struct __SCDynamicStore *SCDynamicStoreRef;

CFDictionaryRef SCDynamicStoreCopyProxies(SCDynamicStoreRef store);

#endif
