
## Garbage Collection
The collector is a non-moving, generational mark-sweep. Minor collections stop the program; major ones mostly run alongside it. Nothing is ever moved, so any pointer a program or native holds stays valid for as long as what it points at is reachable.

### The Heap
Every allocation the collector owns and every block of the program's own memory (`new(T, n)`, variadic arrays) is carved from one range of address space reserved at startup. Its size is the most memory the heap may ever hold, set with `-M`/`--max-heap-size` (default `64GB`). Reserving it costs address space, not memory.

The range is handed out in 64KB pages. A run of pages left free for `--gc-return-age` collections in a row (default `3`) has its memory given back to the system, keeping the address range, so the program's resident memory shrinks after a spike rather than staying at its peak.

### Generations
* Young: everything allocated since the last minor collection, and the survivors of that collection.
* Old: everything that has survived two minor collections, or any major one. Objects are promoted where they lie.

A minor collection runs once `-N`/`--nursery-size` bytes (default `8MB`) have been allocated since the last collection. It traces and frees only young objects. A major collection runs once the heap reaches `-g`/`--gc-ratio` of its current size (`-m`/`--heap-size` to start). It traces and frees both generations, so an old object that becomes unreachable, and its `Delete`, wait for the next one. Everything a major collection keeps is old afterwards.

`System.GC::Minor()` and `System.GC::Major()` ask for a collection of that kind, which runs before the caller's next instruction. A minor collection asked for is major all the same if the heap has already reached its threshold. A major collection asked for always stops the program for its whole length.

### Concurrent Major Collections
A major collection the heap's growth asks for marks alongside the program, on a thread of the collector's own:
1. The program stops while what its threads and statics hold is marked, and the cards are cleaned.
2. The marker traces from there while the program runs. What the program allocates meanwhile is left alone by the marker. Minor collections still run, pausing the marker, and collect only what was allocated since the first stop: what the program holds of it is on its threads and statics, on a card dirtied since the first stop, or decoded by a `Mark` of the program's own. They scan the cards without cleaning them. What survives two of them is promoted and handed to the marker to trace; the rest is left for the remark. Once the marker has traced everything, the marker scans the cards the program dirtied meanwhile and cleans them, tracing what they hold, so that the remark has only the cards dirtied since to scan. It makes up to `--gc-preclean-passes` (default `2`) such passes.
3. The program stops again for the remark. Every reached object's own `Mark` runs, and everything the program could have hidden a reference in since the first stop is marked again: its threads and statics, and every dirty card. Then the collection runs the `Delete`s a major one does, and what the program allocated during marking is kept only if it is reachable.
4. The marker sweeps what the remark left unreached while the program runs again. A collection asked for meanwhile waits for the sweep to finish.

The remark is asked for early when the program asks for a major collection, or when the heap has grown since the first stop by `--gc-remark-growth` (default `1`) of its size. Compiling with `--gc-concurrent=false` makes every major collection stop the program for its whole length instead.

`Delete`s of a concurrent collection run at the remark, on the marker's thread.

### Parallel Marking
A major collection marks on several threads at once, both alongside the program and while it is stopped: the marker, or the thread collecting, and helper threads the collector starts the first time it needs them. Each traces from work of its own and hands half of it to any that has run out. The helpers are woken only once the first thread has traced a thousand or so objects with more left, so a small heap is marked on one thread. The minor collections keep to one thread. `--gc-markers` sets how many threads mark, up to 64. The default, `0`, is half the processors, up to 8, and `--gc-markers=1` marks on one thread.

While the program is stopped, the `Mark`s of different objects run at the same time on different threads. A `Mark` that writes anything shared other than through `mark` has to guard it.

Objects whose class has a `Delete` of the program's own are kept on lists of their own, so a collection finds the ones to run without looking at any other object. Unreachable objects with `Obj`'s own `Delete` are simply freed.

### What a Minor Collection Reaches
A minor collection keeps every young object a major collection would, by two means.

* The write barrier. Every store into the heap marks the 512-byte card it lands on as dirty, and a minor collection scans dirty cards for anything pointing into the young generation. The scan does not know what the words it reads are, so a number that looks like a young address keeps that allocation alive until the next major collection.
* Old `Mark`s. A `Mark` may decide what is reachable from anything at all: an encoded field, memory it only points at, memory of the program's own. Old objects whose marking runs a `Mark` other than `Obj`'s own, directly or through a value they hold inline, are kept on a list of their own, and a minor collection runs every one of them again. Old objects that only use `Obj`'s `Mark` are covered by the card scan alone.

Programs whose old generation holds many objects with their own `Mark`, the standard containers among them, pay for those `Mark`s in every minor collection, and in the remark rather than alongside the program.

### Marks in a Concurrent Collection
The markers run only `Obj`'s own `Mark`, which reads nothing but what an object's type says it holds. An object whose marking runs a `Mark` of the program's own is left reached and untraced, and its `Mark` runs once, at the remark, with the program stopped, so it sees nothing half-changed.

The remark may stop the program between any two of its instructions, not only where it allocates. A `Mark` has to cope with an object whose fields are not yet set, and a reference held only where a `Mark` decodes it has to be somewhere that `Mark` reads at every moment, not only once a sequence of stores is done.
Natives must keep the write barrier:
* A pointer read with `zam_native_argument_pointer` has its card and the next dirtied when it is read and again when the native returns, which covers writing a result through it.
* Writing references anywhere else in the heap must be followed by `zam_native_write_barrier` over what was written.

### Limitations
* A reference held only as an integer, outside anything whose `Mark` turns it back into one, is never a root, and what it names is freed by the next collection.
* An explicitly freed old object is reclaimed at the next major collection.
* An object a method is called on that no variable, field or other location names, such as one a call just returned, is kept reachable until the statement or condition it was called in ends, so what the method hands out of it can be used in the rest of that expression.
* A variadic call's arguments are kept reachable until it returns, whatever the callee does with its array. After that the array is the program's own memory, and what it holds is reachable only through a `Mark` that reads it.

### Verification
Setting `ZAM_GC_VERIFY` in the environment makes every collection first mark as a minor collection would, then mark in full, and stop the program if the minor collection would have freed anything reachable. Every collection is then major. A concurrent collection, after its remark, likewise marks in full with the program stopped and stops it if the remark would have freed anything reachable. A minor collection alongside the marker marks in full the same way, and gives every allocation it did not collect back the state the marker left it in. Every marker yields after each object it traces, to give the program more chances to change what it is looking at.

### Statistics
Setting `ZAM_GC_STATS` in the environment makes every collection write a line to stderr: its number and kind, how long it paused the program, the heap before and after, how much was young, how much survived young and how much was promoted, how many dirty cards were scanned, how many old objects' own `Mark`s were run again, how many objects the helper threads traced, and how many `Delete`s of the program's own were run, and how many bytes of free pages were given back to the system. A concurrent collection's line also gives the first stop, the marking alongside the program, the remark, the sweep alongside the program, and how many cards the marker cleaned before the remark; its pause is the two stops together, and its `cards=` is what the remark scanned. A minor collection run while the marker runs is marked `alongside=1`.
