package build_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// benchmarks are programs timed by BenchmarkPrograms, each writing a checksum
// of what it computed.
var benchmarks = []struct {
	name   string
	source string
}{
	{"numeric", `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var int total = 0;
        for (var int n = 1; n < 300000; n++) {
            var int x = n;
            for (var int s = 0; x != 1; s++) {
                if (x % 2 == 0) {
                    x = x / 2;
                } else {
                    x = 3 * x + 1;
                }
                total++;
            }
        }
        File::StdOut.WriteStr(total{str} + "\n");
    }
}
`},
	{"alloc", `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Node {
        var int value;
        var Node* next;
    }

    fun void(uint, str...) main = (argC, argV) {
        var int total = 0;
        for (var int round = 0; round < 20; round++) {
            var Node* head = null;
            for (var int i = 0; i < 100000; i++) {
                var Node* n = new(Node);
                n.value = i;
                n.next = head;
                head = n;
            }
            var Node* n = head;
            for (var int i = 0; i < 100000; i++) {
                total = total + n.value % 7;
                n = n.next;
            }
        }
        File::StdOut.WriteStr(total{str} + "\n");
    }
}
`},
	{"dispatch", `import std("lang/*");
import std("io/local/*");

namespace Main {
    abstract Shape {
        virtual fun int(int) Area;
    }

    class Square <: Shape* {
        var int side;

        fun int(int) Area = (k) {
            return this.side * this.side + k;
        }
    }

    class Rect <: Shape* {
        var int w;
        var int h;

        fun int(int) Area = (k) {
            return this.w * this.h - k;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Square* square = new(Square);
        square.side = 3;
        var Rect* rect = new(Rect);
        rect.w = 2;
        rect.h = 5;
        var Shape* a = square;
        var Shape* b = rect;
        var int total = 0;
        for (var int i = 0; i < 3000000; i++) {
            var Shape* s = a;
            if (i % 3 == 0) {
                s = b;
            }
            total = total + s.Area(i % 5);
        }
        File::StdOut.WriteStr(total{str} + "\n");
    }
}
`},
	{"strings", `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var int total = 0;
        for (var int i = 0; i < 200000; i++) {
            var str s = i{str} + ":" + (i * 7){str};
            total = total + Strs::CharLen(s){int};
            if (s == "12345:86415") {
                total++;
            }
        }
        File::StdOut.WriteStr(total{str} + "\n");
    }
}
`},
}

// BenchmarkPrograms times each of benchmarks unoptimized and at the default
// optimization, each run a process of its own, holding both to one output.
func BenchmarkPrograms(b *testing.B) {
	for _, bench := range benchmarks {
		var want string
		for _, level := range []string{"0", "2"} {
			b.Run(bench.name+"/O"+level, func(b *testing.B) {
				binary := compileWith(b, []string{"-O" + level, "-m", "16MB", "-N", "8MB"}, bench.source)
				stdout, stderr, status := run(b, binary)
				require.Zero(b, status, stderr)
				if want == "" {
					want = stdout
				}
				require.Equal(b, want, stdout)
				for b.Loop() {
					run(b, binary)
				}
			})
		}
	}
}
