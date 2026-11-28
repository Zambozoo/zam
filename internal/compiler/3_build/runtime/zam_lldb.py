"""lldb formatters for programs zam compiles natively.

DWARF describes classes, structs, tuples and enums itself; these cover what it
cannot: the text of a str, what an any holds, and the function a closure runs.
"""

import lldb

# A str points at a ZamString: its mark word, its length, then its bytes.
STRING_LENGTH = 8
STRING_BYTES = 16
STRING_SHOWN = 256

# An object begins with its mark word and its type, and an any's box holds the
# type id of its payload's type, then the payload's own box.
OBJECT_HEADER = 16
TYPE_ID_WORDS = 3

# A closure is an object whose body begins with its entry and its capture count.
CLOSURE_ENTRY = OBJECT_HEADER
CLOSURE_CAPTURES = OBJECT_HEADER + 8

TYPE_NAMES = "zam_debug_type_names"
TYPE_NAME_ENTRY = 24

_type_names = {}


def _word(process, address):
    error = lldb.SBError()
    value = process.ReadUnsignedFromMemory(address, 8, error)
    return value if error.Success() else None


def _address(valobj):
    """The address a reference names: the one member of the struct it is
    described as."""
    return valobj.GetNonSyntheticValue().GetChildAtIndex(0).GetValueAsUnsigned(0)


def _symbol_address(target, name):
    contexts = target.FindSymbols(name)
    if contexts.GetSize() == 0:
        return None
    address = contexts.GetContextAtIndex(0).GetSymbol().GetStartAddress()
    return address.GetLoadAddress(target)


def _names(target, process):
    """The name and box header of the type of each short type id."""
    key = target.GetExecutable().fullpath
    if key in _type_names:
        return _type_names[key]
    names = {}
    table = _symbol_address(target, TYPE_NAMES)
    count_at = _symbol_address(target, TYPE_NAMES + "_count")
    count = _word(process, count_at) if count_at is not None else None
    if table is not None and count is not None:
        error = lldb.SBError()
        for i in range(count):
            entry = table + i * TYPE_NAME_ENTRY
            short = _word(process, entry)
            name = process.ReadCStringFromMemory(_word(process, entry + 8), 512, error)
            header = _word(process, entry + 16)
            if short is not None and error.Success():
                names[short] = (name, header)
    _type_names[key] = names
    return names


def _find_type(target, name):
    if name.endswith("*"):
        pointee = _find_type(target, name[:-1])
        return pointee.GetPointerType() if pointee is not None else None
    found = target.FindFirstType(name)
    return found if found.IsValid() else None


def str_summary(valobj, internal_dict):
    address = _address(valobj)
    if address == 0:
        return "null"
    process = valobj.GetProcess()
    length = _word(process, address + STRING_LENGTH)
    if length is None:
        return "<unreadable>"
    data = b""
    if length > 0:
        error = lldb.SBError()
        data = process.ReadMemory(address + STRING_BYTES, min(length, STRING_SHOWN), error)
        if not error.Success():
            return "<unreadable>"
    text = data.decode("utf-8", "replace")
    text = text.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")
    return '"' + text + ('..."' if length > STRING_SHOWN else '"')


class AnyProvider:
    """Shows what an any holds as its one child, typed as it was boxed."""

    def __init__(self, valobj, internal_dict):
        self.valobj = valobj
        self.value = None

    def update(self):
        self.value = None
        box = _address(self.valobj)
        if box == 0:
            return False
        process = self.valobj.GetProcess()
        target = self.valobj.GetTarget()
        short = _word(process, box + OBJECT_HEADER)
        payload = _word(process, box + OBJECT_HEADER + TYPE_ID_WORDS * 8)
        if short is None or payload is None:
            return False
        entry = _names(target, process).get(short >> 32)
        if entry is None:
            return False
        name, header = entry
        typ = _find_type(target, name)
        if typ is None:
            return False
        self.value = self.valobj.CreateValueFromAddress("value", payload + OBJECT_HEADER - header, typ)
        return False

    def num_children(self):
        return 1 if self.value is not None else 0

    def get_child_index(self, name):
        return 0 if name == "value" else -1

    def get_child_at_index(self, index):
        return self.value if index == 0 else None

    def has_children(self):
        return True


def any_summary(valobj, internal_dict):
    if _address(valobj) == 0:
        return "null"
    value = valobj.GetChildMemberWithName("value")
    if not value.IsValid():
        return "any"
    shown = value.GetSummary() or value.GetValue() or ""
    return "any(" + value.GetTypeName() + ") " + shown


def closure_summary(valobj, internal_dict):
    address = _address(valobj)
    if address == 0:
        return "null"
    process = valobj.GetProcess()
    target = valobj.GetTarget()
    entry = _word(process, address + CLOSURE_ENTRY)
    captures = _word(process, address + CLOSURE_CAPTURES)
    if entry is None:
        return "<unreadable>"
    symbol = target.ResolveLoadAddress(entry).GetSymbol()
    name = symbol.GetName() if symbol.IsValid() else hex(entry)
    return name + " (" + str(captures) + " captures)"


def __lldb_init_module(debugger, internal_dict):
    module = __name__
    debugger.HandleCommand("type summary add -w zam -F " + module + ".str_summary str")
    debugger.HandleCommand("type synthetic add -w zam -l " + module + ".AnyProvider any")
    debugger.HandleCommand("type summary add -w zam -F " + module + ".any_summary any")
    debugger.HandleCommand(
        "type summary add -w zam -F " + module + ".closure_summary -x '^[^(].*\\(.*\\)$'")
    debugger.HandleCommand("type category enable zam")
