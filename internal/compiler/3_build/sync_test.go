package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncPrimitives contends every sync type across threads that allocate
// enough to collect while others wait on a lock.
func TestSyncPrimitives(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("sync/*");

namespace Main {
    class Counter {
        var int n;
    }

    static var Mutex* lock = null;
    static var int active = 0;
    static var int peak = 0;
    static var int hits = 0;

    static fun int(int) churn = (n) {
        var int total = 0;
        for(var int i = 0; i < n; i++) {
            var str s = i{str} + "-churn";
            total = total + Strs::ByteLen(s);
        }
        return total;
    }

    static fun int(Mutex*, Counter*, int) bump = (m, c, n) {
        for(var int i = 0; i < n; i++) {
            Main::churn(3);
            m.Lock();
            c.n++;
            m.Unlock();
        }
        return n;
    }

    static fun bool(int, int) succ = (cur, v) {
        return v == cur + 1;
    }

    static fun int(Atomic[int]*, RWMutex*, Counter*, int) climb = (a, rw, c, n) {
        for(var int i = 0; i < n; i++) {
            for(var bool done = false; !done;) {
                done = a.Update(a.Load() + 1, Main::succ).1;
            }
            rw.Lock();
            c.n++;
            rw.Unlock();
            rw.RLock();
            var int seen = c.n;
            rw.RUnlock();
            if(seen < 1) {
                System::Panic("RWMutex lost a write");
            }
            Main::churn(2);
        }
        return n;
    }

    static fun void() tick = () {
        Main::lock.Lock();
        Main::active++;
        if(Main::active > Main::peak) {
            Main::peak = Main::active;
        }
        Main::lock.Unlock();
        Main::churn(200);
        Main::lock.Lock();
        Main::active--;
        Main::lock.Unlock();
    }

    static fun void() hit = () {
        Main::hits++;
    }

    static fun int(Once*, Latch*) start = (o, l) {
        Main::churn(50);
        o.Do(Main::hit);
        l.CountDown();
        return 0;
    }

    static fun int(Barrier*, int) rounds = (b, n) {
        var int last = 0;
        for(var int i = 0; i < n; i++) {
            Main::churn(5);
            if(b.Wait()) {
                last++;
            }
        }
        return last;
    }

    static fun int(Semaphore*, int) post = (s, n) {
        for(var int i = 0; i < n; i++) {
            Main::churn(20);
            s.Unlock();
        }
        return n;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Mutex* m = Mutex::New();
        var Counter* c = new(Counter);
        var thread[int] a = async(Main::bump(m, c, 300));
        var thread[int] b = async(Main::bump(m, c, 300));
        var thread[int] d = async(Main::bump(m, c, 300));
        var int w = wait(a) + wait(b) + wait(d);
        var str out = c.n{str};
        out = out + " " + m.TryLock(){str} + " " + m.TryLock(){str};
        m.Unlock();

        var Atomic[int]* at = Atomic::[int]New(0);
        var RWMutex* rw = RWMutex::New();
        var Counter* rc = new(Counter);
        a = async(Main::climb(at, rw, rc, 200));
        b = async(Main::climb(at, rw, rc, 200));
        w = wait(a) + wait(b);
        out = out + " " + at.Load(){str} + " " + rc.n{str} + " " + at.Set(7){str} + " " + at.Load(){str};

        Main::lock = Mutex::New();
        var WaitGroup* wg = WaitGroup::NewWithMaxSize(2U);
        for(var int i = 0; i < 6; i++) {
            wg.Do(Main::tick);
        }
        wg.Wait();
        out = out + " " + (Main::peak <= 2){str} + " " + Main::active{str};

        var Once* o = Once::New();
        var Latch* l = Latch::New(3U);
        a = async(Main::start(o, l));
        b = async(Main::start(o, l));
        d = async(Main::start(o, l));
        l.Wait();
        out = out + " " + Main::hits{str} + " " + l.Count(){str};
        w = wait(a) + wait(b) + wait(d);

        var Barrier* br = Barrier::New(3U);
        a = async(Main::rounds(br, 40));
        b = async(Main::rounds(br, 40));
        d = async(Main::rounds(br, 40));
        out = out + " " + (wait(a) + wait(b) + wait(d)){str};

        var Semaphore* s = Semaphore::New(0U);
        a = async(Main::post(s, 25));
        for(var int i = 0; i < 25; i++) {
            s.Wait();
        }
        w = wait(a);
        out = out + " " + s.TryWait(){str} + "\n";
        File::StdOut.WriteStr(out);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "900 true false 400 400 400 7 true 0 1 0 40 false\n", stdout)
}

// TestMutexRelockPanics locks a mutex the thread already holds.
func TestMutexRelockPanics(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("sync/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var Mutex* m = Mutex::New();
        m.Lock();
        m.Lock();
    }
}
`)
	_, stderr, status := run(t, binary)
	assert.Equal(t, 67, status)
	assert.Contains(t, stderr, "the mutex is already held by this thread")
}
