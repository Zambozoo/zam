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
        var str a = FS::Join(dir, "a");
        var str x = FS::Join(a, "x.txt");
        var str s = (FS::MakeDirs(FS::Join(a, "b/c")) == null){str} + " " + FS::IsDir(FS::Join(a, "b/c")){str};

        s = s + " " + (FS::WriteFile(x, "line1\r\nline2\nlast") == null){str} + " " + (FS::AppendFile(x, "!\n") == null){str};
        var (FS.Info, Err*) info = FS::Stat(x);
        s = s + " " + info.0.size{str} + " " + info.0.isDir{str} + " " + (info.0.modTime.Unix() > 0){str};
        s = s + " " + (FS::Chmod(x, 384) == null){str} + " " + FS::Stat(x).0.mode{str} + "\n";

        var File* f = File::Open(x).0;
        var byte* chunk = new(byte, 16U);
        s = s + f.Seek(7, File.Whence::Start).0{str} + " ";
        var (int, Err*) n = f.Read(chunk, 5);
        s = s + Strs::FromBytes(chunk, n.0) + " " + f.Size().0{str} + " " + f.Seek(-2, File.Whence::End).0{str};
        s = s + " " + (f.Close() == null){str} + " " + f.Close().Err() + "\n";

        var Buffer* b = Buffer::New();
        b.WriteStr(FS::ReadFile(x).0);
        for(var (str, bool) line = b.ReadLine(); line.1; line = b.ReadLine()) {
            s = s + "[" + line.0 + "]";
        }
        for(var int i = 0; i < 5000; i++) {
            b.WriteByte((i % 26 + 97){byte});
        }
        b.ReadStr(4990U);
        s = s + " " + b.Len(){str} + " " + b.Str() + " " + b.ReadByte().0{int}{str} + " " + b.Get(0U){int}{str} + "\n";

        var (Arr[str]*, Err*) entries = FS::ReadDir(a);
        s = s + entries.0{str} + " " + (FS::Rename(x, FS::Join(a, "y.txt")) == null){str} + " " + FS::ReadDir(a).0{str};
        s = s + " " + (FS::Remove(x) != null){str} + " " + (File::Open(x).1 != null){str};
        s = s + " " + (FS::RemoveAll(a) == null){str} + " " + FS::Exists(a){str} + " " + FS::ReadDir(dir).0.Len(){str} + "\n";

        var Pipe* p = Pipe::New().0;
        p.Writer().WriteStr("ping");
        p.Writer().Close();
        s = s + p.Reader().ReadAll().0 + " " + (p.Close() == null){str};

        s = s + " " + (FS::Chdir(dir) == null){str} + " " + (FS::WriteFile("rel.txt", "r") == null){str} + " " + FS::Exists(FS::Join(dir, "rel.txt")){str};
        s = s + " " + (FS::Cwd().1 == null){str} + " " + (Strs::ByteLen(FS::TempDir()) > 0){str} + "\n";
        File::StdOut.WriteStr(s);
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
