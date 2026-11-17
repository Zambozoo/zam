// The part of CoreFoundation the libraries in a macOS target pack call.
#ifndef ZAM_STUB_COREFOUNDATION_H
#define ZAM_STUB_COREFOUNDATION_H

typedef const void *CFTypeRef;
typedef const struct __CFDictionary *CFDictionaryRef;

void CFRelease(CFTypeRef cf);

#endif
