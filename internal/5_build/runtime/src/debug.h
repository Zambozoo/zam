#pragma once

#include "vm.h"

typedef struct ZamDebugger ZamDebugger;

// zam_debug_connect puts the program under the debugger its environment
// names, if any: one already connected on the descriptor ZAM_DEBUG_FD names,
// or one listening on the loopback port ZAM_DEBUG_PORT names, connected to
// with hello <token> program, the token being ZAM_DEBUG_TOKEN. Where
// ZAM_DEBUG_STATUS_PORT names a port too, the process first forks, and what
// returns is the child: the parent connects with hello <token> status, says
// pid <pid> and then exited <status> once the child has ended, and ends the
// same way. That is how a debugger learns of a program something else started,
// as an editor's terminal does.
void zam_debug_connect(ZamVM *vm);

// zam_debug_start puts the program under the debugger on the other end of fd,
// stopped before its first instruction until the debugger lets it go.
//
// The debugger speaks in lines of ASCII, each a word and then numbers in
// decimal. It sends:
//
//   break <offset>...         stop any thread about to run one of these
//   continue                  let every thread go
//   pause                     stop every thread
//   step <thread> in|over|out let every thread go until thread has stepped
//   threads                   answered by threads <thread>...
//   stack <thread>            answered by stack <thread> <frame>...
//   words <thread> <index> <count>
//                             answered by words <word>...
//   read <thread> <address> <count>
//                             answered by read <hex>
//   write <thread> <address> <hex>
//                             answered by write 1, or write 0 if it could not
//   string <thread> <address> [<hex>]
//                             answered by string <string>, or string 0
//   failures 1|0              stop, or not, when a thread fails
//   statics                   answered by statics <address>, where the
//                             static region lies
//   receiver <thread> <entry> answered by receiver <address>
//   instance <thread> <address> <template>
//                             answered by instance <type>
//
// and is sent stopped <thread> <reason> <offset> once every thread has
// stopped, where reason is breakpoint, step or pause and offset is the
// instruction the thread stopped before. A thread failing sends stopped
// <thread> exception <offset> <hex>, the hex being what it failed with. A frame is three numbers: an offset
// inside the instruction it stands at, the operand its slots begin at, and the
// mark offset of the entry its call made. words are a thread's operands, read
// reads bytes in the heap, the static region or, at a frame location, the
// thread's operands, write writes them there, string stores there a new
// string of the bytes given, allocated by the thread, receiver answers a call's receiver, and instance answers the instantiation of a generic
// declaration the instance at an address is, as a short type id. Each answers empty while the program runs. Closing the connection lets the program run
// on as though there were no debugger.
void zam_debug_start(ZamVM *vm, int fd);

// zam_debug_check is run before each instruction. It holds the thread there
// while the program is stopped, and stops the program there at a breakpoint or
// at the end of the thread's step.
void zam_debug_check(ZamThread *thread);

// zam_debug_fail holds a thread failing with message while the debugger looks
// at it, if the debugger asked to stop on failures, and then returns for it to
// fail. A thread holding one of the runtime's own locks is not held, holding
// it keeping every other from stopping. It is meant to be run in a signal
// handler too.
void zam_debug_fail(ZamThread *thread, const char *message);

// zam_debug_hold holds a thread back from coming out of a blocking call while
// the program is stopped, so that its stacks stay still while they are read.
void zam_debug_hold(ZamThread *thread);
