package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalIO works through files, directories, buffers and pipes in a
// directory of the test's own.
func TestLocalIO(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var str dir = argV[0];
        var str a = type(FS).Join(dir, "a");
        var str x = type(FS).Join(a, "x.txt");
        var str s = (type(FS).MakeDirs(type(FS).Join(a, "b/c")) == null){str} + " " + type(FS).IsDir(type(FS).Join(a, "b/c")){str};

        s = s + " " + (type(FS).WriteFile(x, "line1\r\nline2\nlast") == null){str} + " " + (type(FS).AppendFile(x, "!\n") == null){str};
        var Pair[FS.Info, Err*] info = type(FS).Stat(x);
        s = s + " " + info.first.size{str} + " " + info.first.isDir{str} + " " + (info.first.modTime.Unix() > 0){str};
        s = s + " " + (type(FS).Chmod(x, 384) == null){str} + " " + type(FS).Stat(x).first.mode{str} + "\n";

        var File* f = type(File).Open(x).first;
        var byte* chunk = new(byte, 16U);
        s = s + f.Seek(7, type(File.Whence).Start).first{str} + " ";
        var Pair[int, Err*] n = f.Read(chunk, 5);
        s = s + type(Strs).FromBytes(chunk, n.first) + " " + f.Size().first{str} + " " + f.Seek(-2, type(File.Whence).End).first{str};
        s = s + " " + (f.Close() == null){str} + " " + f.Close().Err() + "\n";

        var Buffer* b = type(Buffer).New();
        b.WriteStr(type(FS).ReadFile(x).first);
        for(var Pair[str, bool] line = b.ReadLine(); line.second; line = b.ReadLine()) {
            s = s + "[" + line.first + "]";
        }
        for(var int i = 0; i < 5000; i++) {
            b.WriteByte((i % 26 + 97){byte});
        }
        b.ReadStr(4990U);
        s = s + " " + b.Len(){str} + " " + b.Str() + " " + b.ReadByte().first{int}{str} + " " + b.Get(0U){int}{str} + "\n";

        var Pair[Arr[str]*, Err*] entries = type(FS).ReadDir(a);
        s = s + entries.first{str} + " " + (type(FS).Rename(x, type(FS).Join(a, "y.txt")) == null){str} + " " + type(FS).ReadDir(a).first{str};
        s = s + " " + (type(FS).Remove(x) != null){str} + " " + (type(File).Open(x).second != null){str};
        s = s + " " + (type(FS).RemoveAll(a) == null){str} + " " + type(FS).Exists(a){str} + " " + type(FS).ReadDir(dir).first.Len(){str} + "\n";

        var Pipe* p = type(Pipe).New().first;
        p.Writer().WriteStr("ping");
        p.Writer().Close();
        s = s + p.Reader().ReadAll().first + " " + (p.Close() == null){str};

        s = s + " " + (type(FS).Chdir(dir) == null){str} + " " + (type(FS).WriteFile("rel.txt", "r") == null){str} + " " + type(FS).Exists(type(FS).Join(dir, "rel.txt")){str};
        s = s + " " + (type(FS).Cwd().second == null){str} + " " + (type(Strs).ByteLen(type(FS).TempDir()) > 0){str} + "\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, t.TempDir())
	require.Zero(t, status, stderr)
	assert.Equal(t, "true true true true 19 false true true 384\n"+
		"7 line2 19 17 true file already closed\n"+
		"[line1][line2][last!] 10 yzabcdefgh 121 122\n"+
		"[b, x.txt] true [b, y.txt] true true true false 0\n"+
		"ping true true true true true true\n", stdout)
}
