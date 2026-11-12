
## Garbage Collection
The collector is a stop-the-world, non-moving, generational mark-sweep. Nothing is ever moved, so any pointer a program or native holds stays valid for as long as what it points at is reachable.

### The Heap
Every allocation the collector owns and every block of the program's own memory (`new(T, n)`, variadic arrays) is carved from one range of address space reserved at startup. Its size is the most memory the heap may ever hold, set with `-M`/`--max-heap-size` (default `64GB`). Reserving it costs address space, not memory.

The range is handed out in 64KB pages. A run of pages left free for three collections in a row has its memory given back to the system, keeping the address range, so the program's resident memory shrinks after a spike rather than staying at its peak.

### Generations
* Young: everything allocated since the last minor collection, and the survivors of that collection.
* Old: everything that has survived two minor collections, or any major one. Objects are promoted where they lie.

A minor collection runs once `-N`/`--nursery-size` bytes (default `8MB`) have been allocated since the last collection. It traces and frees only young objects. A major collection runs once the heap reaches `-g`/`--gc-ratio` of its current size (`-m`/`--heap-size` to start). It traces and frees both generations, so an old object that becomes unreachable, and its `Delete`, wait for the next one.

`System.GC::Minor()` and `System.GC::Major()` ask for a collection of that kind, which runs before the caller's next instruction. A minor collection asked for is major all the same if the heap has already reached its threshold.

Objects whose class has a `Delete` of the program's own are kept on lists of their own, so a collection finds the ones to run without looking at any other object. Unreachable objects with `Obj`'s own `Delete` are simply freed.

### What a Minor Collection Reaches
A minor collection keeps every young object a major collection would, by two means.

* The write barrier. Every store into the heap marks the 512-byte card it lands on as dirty, and a minor collection scans dirty cards for anything pointing into the young generation. The scan does not know what the words it reads are, so a number that looks like a young address keeps that allocation alive until the next major collection.
* Old `Mark`s. A `Mark` may decide what is reachable from anything at all: an encoded field, memory it only points at, memory of the program's own. Old objects whose marking runs a `Mark` other than `Obj`'s own, directly or through a value they hold inline, are kept on a list of their own, and a minor collection runs every one of them again. Old objects that only use `Obj`'s `Mark` are covered by the card scan alone.

Programs whose old generation holds many objects with their own `Mark`, the standard containers among them, pay for those `Mark`s in every minor collection.

A card that held a young address when a minor collection scanned it, and every card of an object it promotes, is left dirty for the next, since what they point at may still be young.

Natives must keep the write barrier:
* A pointer read with `zam_native_argument_pointer` already has its card and the next dirtied, which covers writing a result through it.
* Writing references anywhere else in the heap must be followed by `zam_native_write_barrier` over what was written.

### Limitations
* A reference held only as an integer, outside anything whose `Mark` turns it back into one, is never a root, and what it names is freed by the next collection.
* An explicitly freed old object is reclaimed at the next major collection.

### Verification
Setting `ZAM_GC_VERIFY` in the environment makes every collection first mark as a minor collection would, then mark in full, and stop the program if the minor collection would have freed anything reachable. Every collection is then major.

### Statistics
Setting `ZAM_GC_STATS` in the environment makes every collection write a line to stderr: its number and kind, how long it paused the program, the heap before and after, how much was young, how much survived young and how much was promoted, how many dirty cards were scanned, how many old objects' own `Mark`s were run again, and how many `Delete`s of the program's own were run, and how many bytes of free pages were given back to the system.
