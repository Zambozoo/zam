package build_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archiveFixture is one entry the Go side writes for zam to read, and expects
// back from what zam writes.
type archiveFixture struct {
	name string
	mode int64
	body string
	dir  bool
}

var archiveFixtures = []archiveFixture{
	{name: "docs/", mode: 0o755, dir: true},
	{name: "docs/readme.md", mode: 0o644, body: "# hi\n"},
	{name: "run.sh", mode: 0o755, body: "echo hi\n"},
}

var archiveTime = time.Unix(1700000000, 0)

func writeTarFixture(t *testing.T, w io.Writer) {
	t.Helper()
	tw := tar.NewWriter(w)
	for _, f := range archiveFixtures {
		header := &tar.Header{Name: f.name, Mode: f.mode, ModTime: archiveTime, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if f.dir {
			header.Typeflag = tar.TypeDir
		}
		require.NoError(t, tw.WriteHeader(header))
		_, err := tw.Write([]byte(f.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
}

func writeZipFixture(t *testing.T, w io.Writer) {
	t.Helper()
	zw := zip.NewWriter(w)
	for _, f := range archiveFixtures {
		header := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: archiveTime}
		mode := os.FileMode(f.mode)
		if f.dir {
			mode |= os.ModeDir
			header.Method = zip.Store
		}
		header.SetMode(mode)
		fw, err := zw.CreateHeader(header)
		require.NoError(t, err)
		_, err = fw.Write([]byte(f.body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
}

// TestArchives reads tar, gzip-compressed tar and zip archives written by Go's
// own encoders, writes tar and zip archives that Go's decoders read back, and
// rejects input that is not an archive.
func TestArchives(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	var plain bytes.Buffer
	writeTarFixture(t, &plain)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "in.tar"), plain.Bytes(), 0o600))
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write(plain.Bytes())
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "in.tgz"), compressed.Bytes(), 0o600))
	var zipped bytes.Buffer
	writeZipFixture(t, &zipped)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "in.zip"), zipped.Bytes(), 0o600))

	binary := compile(t, `import std("lang/*");
import std("lang/time/*");
import std("io/local/*");
import std("codec/archive/*");

namespace Main {
    static fun str(Arr[Archive.Entry*]*) describe = (entries) {
        var str s = "";
        for(var uint i = 0U; i < entries.Len(); i++) {
            var Archive.Entry* e = entries.Get(i);
            var str body = "-";
            if(e.data != null) {
                body = Strs::FromBytes(e.data.Arr(), e.data.Len(){int});
            }
            s = s + "[" + e.name + " " + e.isDir{str} + " " + e.mode{str} + " " + e.modTime.Unix(){str} + " " + body + "]";
        }
        return s;
    }

    static fun Arr[byte]*(str) bytesOf = (s) {
        var (byte*, int) p = Strs::ToBytes(s);
        var byte* b = new(byte, p.1{uint});
        for(var int i = 0; i < p.1; i++) {
            b[i] = p.0[i];
        }
        return Arr::[byte]New(p.1{uint}, b...);
    }

    static fun void(str, Arr[byte]*) save = (path, b) {
        var File* f = File::Create(path).0;
        f.Write(b.Arr(), b.Len(){int});
        f.Close();
    }

    fun void(uint, str...) main = (argC, argV) {
        var str dir = argV[0];
        var str tar = FS::ReadFile(FS::Join(dir, "in.tar")).0;
        var str tgz = FS::ReadFile(FS::Join(dir, "in.tgz")).0;
        var str zip = FS::ReadFile(FS::Join(dir, "in.zip")).0;
        var (byte*, int) tb = Strs::ToBytes(tar);
        var (byte*, int) gb = Strs::ToBytes(tgz);
        var (byte*, int) zb = Strs::ToBytes(zip);
        var str s = Main::describe(TAR::Read(tb.0, tb.1{uint}).0) + "\n";
        s = s + Main::describe(TAR::Read(gb.0, gb.1{uint}).0) + "\n";
        s = s + Main::describe(ZIP::Read(zb.0, zb.1{uint}).0) + "\n";

        var Arr[Archive.Entry*]* out = Arr::[Archive.Entry*]New(3U, new(Archive.Entry*, 3U)...);
        out.Set(0U, Archive.Entry::Dir("d/"));
        out.Set(1U, Archive.Entry::File("d/a.txt", Main::bytesOf("hello")));
        var Archive.Entry* tool = Archive.Entry::File("tool", Main::bytesOf("#!/bin/sh\n"));
        tool.mode = 493;
        tool.modTime = Time::FromUnix(1700000002, 0);
        out.Set(2U, tool);
        var Arr[byte]* written = TAR::Write(out).0;
        var Arr[byte]* zipWritten = ZIP::Write(out).0;
        Main::save(FS::Join(dir, "out.tar"), written);
        Main::save(FS::Join(dir, "out.zip"), zipWritten);
        s = s + Main::describe(TAR::Read(written.Arr(), written.Len()).0) + "\n";
        s = s + Main::describe(ZIP::Read(zipWritten.Arr(), zipWritten.Len()).0) + "\n";

        var Arr[byte]* junk = Main::bytesOf("this is not an archive at all, not even close to one");
        s = s + (TAR::Read(junk.Arr(), junk.Len()).1 != null){str} + " " + (ZIP::Read(junk.Arr(), junk.Len()).1 != null){str};
        s = s + " " + (ZIP::Read(written.Arr(), written.Len()).1 != null){str} + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, dir)
	require.Zero(t, status, stderr)
	fixtures := "[docs/ true 493 1700000000 -][docs/readme.md false 420 1700000000 # hi\n][run.sh false 493 1700000000 echo hi\n]"
	written := "[d/ true 493 0 -][d/a.txt false 420 0 hello][tool false 493 1700000002 #!/bin/sh\n]"
	assert.Equal(t, fixtures+"\n"+fixtures+"\n"+fixtures+"\n"+written+"\n"+written+"\n"+"true true true\n", stdout)

	want := map[string]string{"d/": "", "d/a.txt": "hello", "tool": "#!/bin/sh\n"}

	raw, err := os.ReadFile(filepath.Join(dir, "out.tar"))
	require.NoError(t, err)
	tr := tar.NewReader(bytes.NewReader(raw))
	got := map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		got[header.Name] = string(body)
		if header.Name == "tool" {
			assert.Equal(t, int64(0o755), header.Mode&0o7777)
			assert.Equal(t, int64(1700000002), header.ModTime.Unix())
		}
	}
	assert.Equal(t, want, got)

	raw, err = os.ReadFile(filepath.Join(dir, "out.zip"))
	require.NoError(t, err)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	got = map[string]string{}
	var names []string
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		got[f.Name] = string(body)
		names = append(names, f.Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"d/", "d/a.txt", "tool"}, names)
	assert.Equal(t, want, got)
}
