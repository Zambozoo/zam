package build_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base/target"
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
        var (uint, Err*) n = f.Read(chunk, 5U);
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
        s = s + " " + (FS::Cwd().1 == null){str} + " " + FS::Exists(FS::TempDir()){str} + "\n";
        s = s + FS::Join(FS::Join("", "p"), "q") + " " + FS::Join("p/", "q") + " " + FS::Separator + "\n";
        s = s + FS::ToSlash(FS::Join("a", "b")) + " " + FS::FromSlash("a/b") + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, t.TempDir())
	require.Zero(t, status, stderr)
	// A Windows file has only a read-only flag, so 0600 reads back as 0666, and
	// its paths separate with a backslash.
	mode, sep := "384", "/"
	if testTarget().OS == target.Windows {
		mode, sep = "438", `\`
	}
	assert.Equal(t, "true true true true 19 false true true "+mode+"\n"+
		"7 line2 19 17 true file already closed\n"+
		"[line1][line2][last!] 10 yzabcdefgh 121 122\n"+
		"[b, x.txt] true [b, y.txt] true true true false 0\n"+
		"ping true true true true true true\n"+
		"p"+sep+"q p/q "+sep+"\n"+
		"a/b a"+sep+"b\n", stdout)
}

// TestProcess runs programs: one writing to stdout and stderr and exiting with
// a code, one in a directory of its own with a variable set, and one that does
// not exist. It also finds a symbolic link and makes temporary directories.
func TestProcess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	canLink := os.Symlink(dir, link) == nil
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var str dir = argV[0];
        var bool windows = FS::Separator == "\\";
        var Arr[str]* none = Arr::[str]New(0U, new(str, 0U)...);
        var Arr[str]* writes = Arr::[str]New(3U, "sh", "-c", "printf out; printf err >&2; exit 3");
        var Arr[str]* where = Arr::[str]New(3U, "sh", "-c", "printf \"$ZAM_PROCESS_TEST \"; pwd -P");
        if(windows) {
            writes = Arr::[str]New(3U, "cmd", "/c", "echo out& echo err 1>&2& exit 3");
            where = Arr::[str]New(3U, "cmd", "/c", "echo %ZAM_PROCESS_TEST%& cd");
        }
        var (Process.Result, Err*) a = Process::Run("", writes, none);
        var str s = (a.1 == null){str} + " " + a.0.code{str} + " " + Strs::TrimSpace(a.0.output) + "\n";
        var (Process.Result, Err*) b = Process::Run(dir, where, Arr::[str]New(1U, "ZAM_PROCESS_TEST=set"));
        s = s + (b.1 == null){str} + " " + b.0.code{str} + " " + Strs::TrimSpace(b.0.output) + "\n";
        var (Process.Result, Err*) c = Process::Run("", Arr::[str]New(1U, "zam-no-such-program"), none);
        s = s + (c.1 != null){str} + "\n";

        s = s + FS::IsLink(FS::Join(dir, "link")){str} + " " + FS::IsLink(dir){str} + "\n";
        var (str, Err*) one = FS::MakeTempDir(dir, ".t-");
        var (str, Err*) two = FS::MakeTempDir(dir, ".t-");
        s = s + (one.1 == null && two.1 == null){str} + " " + (one.0 != two.0){str} + " " + FS::IsDir(one.0){str} + " " + Strs::HasPrefix(one.0, FS::Join(dir, ".t-")){str} + "\n";
        s = s + System::OS() + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, dir)
	require.Zero(t, status, stderr)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	written, where := "outerr", "set "+resolved
	if testTarget().OS == target.Windows {
		written, where = "out\r\nerr", "set\r\n"+dir
	}
	links := "false false"
	if canLink {
		links = "true false"
	}
	assert.Equal(t, "true 3 "+written+"\n"+"true 0 "+where+"\n"+"true\n"+links+"\n"+"true true true true\n"+goos(testTarget())+"\n", stdout)
}

// goos answers the operating system t is for, as Go names it.
func goos(t target.Target) string {
	switch t.OS {
	case target.Windows:
		return "windows"
	case target.MacOS:
		return "darwin"
	}
	return "linux"
}
