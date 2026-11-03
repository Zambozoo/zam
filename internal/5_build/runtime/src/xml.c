#include <libxml/parser.h>
#include <libxml/tree.h>
#include <stdio.h>

#include "codec.h"

// The XML natives, over libxml2. A value is an element node. Its document is
// shared by every value in it and counts them in _private, which libxml2
// leaves to the application, being freed with the last.

static xmlNodePtr nodeArgument(ZamThread *thread, uint64_t wordIndex) {
    xmlNodePtr n = codec_handle(thread, wordIndex);
    if (n == NULL)
        zam_native_panic(thread, "XML value was released");
    return n;
}

static void retain(xmlNodePtr n) {
    n->doc->_private = (void *)((intptr_t)n->doc->_private + 1);
}

static void writeNode(ZamThread *thread, uint64_t wordIndex, xmlNodePtr n) {
    if (n != NULL)
        retain(n);
    codec_write_word(thread, wordIndex, (ZamWord)(uintptr_t)n);
}

static xmlChar *nameArgument(ZamThread *thread, uint64_t wordIndex) {
    xmlChar *name = (xmlChar *)codec_c_string(thread, wordIndex);
    if (xmlValidateNameValue(name) == 0) {
        free(name);
        zam_native_panic(thread, "XML name is not a valid name");
    }
    return name;
}

static void writeXmlString(ZamThread *thread, uint64_t wordIndex, xmlChar *text) {
    if (text == NULL) {
        codec_write_string(thread, wordIndex, "", 0);
        return;
    }
    codec_write_string(thread, wordIndex, (const char *)text, strlen((const char *)text));
    xmlFree(text);
}

// stdXmlParse parses a document and answers its root element. Network access
// and diagnostics on stderr are off, and entities are left unexpanded.
static void stdXmlParse(ZamThread *thread) {
    const ZamString *s = codec_string(thread, 0);
    xmlParserCtxtPtr ctxt = xmlNewParserCtxt();
    if (ctxt == NULL)
        zam_native_panic(thread, "out of memory parsing XML");
    xmlDocPtr doc = xmlCtxtReadMemory(ctxt, s->bytes, (int)s->length, NULL, "UTF-8",
                                      XML_PARSE_NONET | XML_PARSE_NOERROR |
                                          XML_PARSE_NOWARNING | XML_PARSE_NOBLANKS);
    xmlNodePtr root = doc == NULL ? NULL : xmlDocGetRootElement(doc);
    if (root == NULL) {
        char message[256] = "XML document has no root element";
        const xmlError *err = xmlCtxtGetLastError(ctxt);
        if (err != NULL && err->message != NULL) {
            snprintf(message, sizeof message, "%s", err->message);
            size_t length = strlen(message);
            while (length > 0 && message[length - 1] == '\n')
                message[--length] = '\0';
            snprintf(message + length, sizeof message - length, " at line %d", err->line);
        }
        if (doc != NULL)
            xmlFreeDoc(doc);
        codec_write_word(thread, 1, 0);
        codec_write_error(thread, 2, message);
    } else {
        doc->_private = NULL;
        writeNode(thread, 1, root);
        codec_write_error(thread, 2, NULL);
    }
    xmlFreeParserCtxt(ctxt);
    zam_native_return_void(thread);
}

static void stdXmlRelease(ZamThread *thread) {
    xmlNodePtr n = codec_handle(thread, 0);
    if (n != NULL) {
        xmlDocPtr doc = n->doc;
        doc->_private = (void *)((intptr_t)doc->_private - 1);
        if (doc->_private == NULL)
            xmlFreeDoc(doc);
    }
    zam_native_return_void(thread);
}

static void stdXmlName(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    codec_write_string(thread, 1, (const char *)n->name, strlen((const char *)n->name));
    zam_native_return_void(thread);
}

// stdXmlText answers the text of an element and everything beneath it.
static void stdXmlText(ZamThread *thread) {
    writeXmlString(thread, 1, xmlNodeGetContent(nodeArgument(thread, 0)));
    zam_native_return_void(thread);
}

static void stdXmlAttr(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    xmlChar *name = (xmlChar *)codec_c_string(thread, 1);
    bool found = xmlHasProp(n, name) != NULL;
    writeXmlString(thread, 2, found ? xmlGetProp(n, name) : NULL);
    codec_write_bool(thread, 3, found);
    free(name);
    zam_native_return_void(thread);
}

static void stdXmlSetAttr(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    xmlChar *name = nameArgument(thread, 1);
    xmlChar *value = (xmlChar *)codec_c_string(thread, 2);
    xmlSetProp(n, name, value);
    free(name);
    free(value);
    zam_native_return_void(thread);
}

static void stdXmlRemoveAttr(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    xmlChar *name = (xmlChar *)codec_c_string(thread, 1);
    xmlUnsetProp(n, name);
    free(name);
    zam_native_return_void(thread);
}

// stdXmlAttrs answers the attribute names in document order, joined by NUL
// bytes.
static void stdXmlAttrs(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    CodecBuffer b = {0};
    for (xmlAttrPtr a = n->properties; a != NULL; a = a->next) {
        if (a != n->properties)
            codec_buffer_append(&b, "", 1);
        codec_buffer_append(&b, a->name, strlen((const char *)a->name));
    }
    codec_write_string(thread, 1, b.bytes, b.length);
    free(b.bytes);
    zam_native_return_void(thread);
}

static void stdXmlChildCount(ZamThread *thread) {
    *(ZamInt *)zam_native_argument_pointer(thread, 1) =
        (ZamInt)xmlChildElementCount(nodeArgument(thread, 0));
    zam_native_return_void(thread);
}

// stdXmlChildAt answers element child i, counting elements only.
static void stdXmlChildAt(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    ZamInt i = (ZamInt)zam_native_argument(thread, 1);
    xmlNodePtr child = xmlFirstElementChild(n);
    for (; child != NULL && i > 0; i--)
        child = xmlNextElementSibling(child);
    if (child == NULL || i < 0)
        zam_native_panic(thread, "XML.ChildAt index out of range");
    writeNode(thread, 2, child);
    zam_native_return_void(thread);
}

// stdXmlChild answers the first element child by name.
static void stdXmlChild(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    const ZamString *name = codec_string(thread, 1);
    xmlNodePtr child = xmlFirstElementChild(n);
    for (; child != NULL; child = xmlNextElementSibling(child))
        if (strlen((const char *)child->name) == name->length &&
            memcmp(child->name, name->bytes, name->length) == 0)
            break;
    writeNode(thread, 2, child);
    codec_write_bool(thread, 3, child != NULL);
    zam_native_return_void(thread);
}

static void stdXmlNew(ZamThread *thread) {
    xmlChar *name = nameArgument(thread, 0);
    xmlDocPtr doc = xmlNewDoc((const xmlChar *)"1.0");
    xmlNodePtr root = xmlNewDocNode(doc, NULL, name, NULL);
    xmlDocSetRootElement(doc, root);
    free(name);
    doc->_private = NULL;
    writeNode(thread, 1, root);
    zam_native_return_void(thread);
}

static void stdXmlAddChild(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    xmlChar *name = nameArgument(thread, 1);
    xmlNodePtr child = xmlNewDocNode(n->doc, NULL, name, NULL);
    xmlAddChild(n, child);
    free(name);
    writeNode(thread, 2, child);
    zam_native_return_void(thread);
}

// stdXmlAppend appends a copy of another element and everything beneath it.
static void stdXmlAppend(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    xmlNodePtr copy = xmlDocCopyNode(nodeArgument(thread, 1), n->doc, 1);
    if (copy == NULL)
        zam_native_panic(thread, "out of memory copying XML");
    xmlAddChild(n, copy);
    zam_native_return_void(thread);
}

// stdXmlSetText replaces an element's children with one text node.
static void stdXmlSetText(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    const ZamString *text = codec_string(thread, 1);
    xmlNodeSetContent(n, NULL);
    xmlAddChild(n, xmlNewDocTextLen(n->doc, (const xmlChar *)text->bytes, (int)text->length));
    zam_native_return_void(thread);
}

static void stdXmlEncode(ZamThread *thread) {
    xmlNodePtr n = nodeArgument(thread, 0);
    xmlBufferPtr buffer = xmlBufferCreate();
    xmlNodeDump(buffer, n->doc, n, 0, zam_native_argument(thread, 1) != 0);
    codec_write_string(thread, 2, (const char *)xmlBufferContent(buffer),
                       (size_t)xmlBufferLength(buffer));
    xmlBufferFree(buffer);
    zam_native_return_void(thread);
}

const ZamNativeEntry zamXmlNativeTable[] = {
    {"STD_XML_PARSE", stdXmlParse},
    {"STD_XML_RELEASE", stdXmlRelease},
    {"STD_XML_NAME", stdXmlName},
    {"STD_XML_TEXT", stdXmlText},
    {"STD_XML_ATTR", stdXmlAttr},
    {"STD_XML_SET_ATTR", stdXmlSetAttr},
    {"STD_XML_REMOVE_ATTR", stdXmlRemoveAttr},
    {"STD_XML_ATTRS", stdXmlAttrs},
    {"STD_XML_CHILD_COUNT", stdXmlChildCount},
    {"STD_XML_CHILD_AT", stdXmlChildAt},
    {"STD_XML_CHILD", stdXmlChild},
    {"STD_XML_NEW", stdXmlNew},
    {"STD_XML_ADD_CHILD", stdXmlAddChild},
    {"STD_XML_APPEND", stdXmlAppend},
    {"STD_XML_SET_TEXT", stdXmlSetText},
    {"STD_XML_ENCODE", stdXmlEncode},
};

const uint64_t zamXmlNativeTableCount =
    sizeof(zamXmlNativeTable) / sizeof(zamXmlNativeTable[0]);
