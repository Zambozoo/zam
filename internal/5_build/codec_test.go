package build_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTextCodecs checks Base64 against the RFC 4648 vectors and Hex in both
// directions, including what each rejects.
func TestTextCodecs(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("codec/text/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var str s = "[" + type(Base64).EncodeStr("") + "] " + type(Base64).EncodeStr("f") + " " + type(Base64).EncodeStr("fo") + " " + type(Base64).EncodeStr("foo");
        s = s + " " + type(Base64).EncodeStr("foob") + " " + type(Base64).EncodeStr("fooba") + " " + type(Base64).EncodeStr("foobar");
        s = s + " " + type(Base64).DecodeStr("Zm9vYmE=").first + " " + type(Base64).DecodeStr("Zm9vYmE").first + " " + type(Base64).DecodeStr("Zm9vYmFy").first;
        s = s + " " + type(Base64).DecodeStr("Zm9*").second.Err() + " " + type(Base64).DecodeStr("Zm9vY").second.Err();
        var byte* b = new(byte, 2U);
        b[0] = 251{byte};
        b[1] = 255{byte};
        var str u = type(Base64).EncodeURL(b, 2U);
        var Arr[byte]* back = type(Base64).DecodeURL(u).first;
        s = s + " " + u + " " + type(Base64).Encode(b, 2U) + " " + back.Get(0U){int}{str} + "," + back.Get(1U){int}{str} + "\n";

        s = s + type(Hex).EncodeStr("hi!") + " " + type(Hex).DecodeStr("48656C6c6F").first + " " + type(Hex).Encode(b, 2U);
        s = s + " " + type(Hex).DecodeStr("abc").second.Err() + " " + type(Hex).DecodeStr("zz").second.Err() + " [" + type(Hex).EncodeStr("") + "]\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "[] Zg== Zm8= Zm9v Zm9vYg== Zm9vYmE= Zm9vYmFy fooba fooba foobar invalid base64 byte at offset 3 invalid base64 length -_8 +/8= 251,255\n"+
		"686921 Hello fbff odd hex length invalid hex byte at offset 0 []\n", stdout)
}

// TestJSON parses, reads, builds and writes JSON through json-c, and parses
// enough documents for the collector to release many of them.
func TestJSON(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("codec/format/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var JSON* doc = type(JSON).Parse("{\"name\": \"zam\", \"tags\": [\"a\", \"b\"], \"n\": 3, \"pi\": 3.5, \"ok\": true, \"none\": null, \"nested\": {\"x\": -1}}").first;
        var str s = doc.Keys(){str} + " " + doc.Len(){str} + " " + doc.Get("name").first.AsStr() + " " + doc.Get("tags").first.At(1U).AsStr();
        s = s + " " + doc.Get("n").first.AsInt(){str} + " " + doc.Get("pi").first.AsFlt(){str} + " " + doc.Get("pi").first.AsInt(){str} + " " + doc.Get("ok").first.AsBool(){str};
        s = s + " " + doc.Get("none").second{str} + " " + doc.Get("none").first.IsNull(){str} + " " + doc.Get("missing").second{str};
        s = s + " " + (doc.Get("nested").first.Kind() == type(JSON.Kind).Object){str} + " " + (doc.Get("n").first.Kind() == type(JSON.Kind).Int){str} + "\n";

        doc.Get("nested").first.Set("y", type(JSON).NewStr("q\"/"));
        doc.Remove("tags");
        doc.Remove("pi");
        s = s + doc{str} + "\n";

        var JSON* built = type(JSON).NewObject();
        var JSON* list = type(JSON).NewArray();
        list.Append(type(JSON).NewBool(false));
        list.Append(null);
        list.Append(type(JSON).NewFlt(0.25));
        list.SetAt(0U, type(JSON).NewInt(-7));
        built.Set("list", list);
        built.Set("empty", type(JSON).NewObject());
        s = s + built.Encode(false) + " " + list.Len(){str} + " " + type(JSON).Null(){str} + "\n";

        s = s + (type(JSON).Parse("{\"a\": [1, 2]}").first == type(JSON).Parse(" { \"a\" : [ 1 , 2 ] } ").first){str};
        s = s + " " + (type(JSON).Parse("[1]").first == type(JSON).Parse("[2]").first){str};
        s = s + " " + (type(JSON).Parse("{\"a\":}").second != null){str} + " " + (type(JSON).Parse("1 2").second != null){str};
        s = s + " " + type(JSON).Parse("  42 ").first.AsInt(){str} + " " + type(JSON).Parse("\"x\"").first{str};
        var int total = 0;
        for(var int i = 0; i < 2000; i++) {
            total = total + type(JSON).Parse("[" + i{str} + ", {\"k\": \"v\"}]").first.At(0U).AsInt();
        }
        s = s + " " + total{str} + "\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "[name, tags, n, pi, ok, none, nested] 7 zam b 3 3.5 3 true true true false true true\n"+
		`{"name":"zam","n":3,"ok":true,"none":null,"nested":{"x":-1,"y":"q\"/"}}`+"\n"+
		`{"list":[-7,null,0.25],"empty":{}} 3 null`+"\n"+
		"true false true true 42 \"x\" 1999000\n", stdout)
}

// TestYAML parses, reads, builds and writes YAML through libyaml, including
// aliases, block scalars and multiple documents, and releases many documents
// under collection.
func TestYAML(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("codec/format/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var str src = "name: zam\nversion: 1.5\ncount: 0x1F\nok: true\nnone: ~\nquoted: \"true\"\nbase: &b {x: 1}\nderived: *b\nlist:\n  - a\n  - -3\n  - .inf\nblock: |\n  line one\n  line two\n";
        var YAML* y = type(YAML).Parse(src).first;
        var str s = y.Keys(){str} + " " + y.Len(){str} + " " + y.Get("name").first.AsStr() + " " + y.Get("version").first.AsFlt(){str};
        s = s + " " + y.Get("count").first.AsInt(){str} + " " + y.Get("ok").first.AsBool(){str} + " " + y.Get("none").first.IsNull(){str};
        s = s + " " + (y.Get("quoted").first.Kind() == type(YAML.Kind).String){str} + " " + y.Get("derived").first.Get("x").first.AsInt(){str};
        s = s + " " + y.Get("list").first.Len(){str} + " " + y.Get("list").first.At(1U).AsInt(){str} + " " + y.Get("list").first.At(2U).AsFlt(){str};
        s = s + " [" + y.Get("block").first.AsStr() + "] " + y.Get("missing").second{str} + "\n";

        var YAML* m = type(YAML).NewMap();
        m.Set("s", type(YAML).NewStr("true"));
        m.Set("n", type(YAML).NewInt(42));
        m.Set("f", type(YAML).NewFlt(0.1));
        var YAML* seq = type(YAML).NewSeq();
        seq.Append(type(YAML).NewBool(false));
        seq.Append(null);
        seq.Append(y.Get("base").first);
        m.Set("seq", seq);
        m.Set("n", type(YAML).NewStr("plain"));
        m.Remove("s");
        s = s + m.Encode() + "|" + type(YAML).Parse(m.Encode()).first.Get("f").first.AsFlt(){str} + "\n";

        var Pair[Arr[YAML*]*, Err*] all = type(YAML).ParseAll("--- 1\n--- [2, 3]\n");
        s = s + all.first.Len(){str} + " " + all.first.Get(1U).At(1U).AsInt(){str} + " " + type(YAML).Parse("").first.IsNull(){str};
        s = s + " " + (type(YAML).Parse("a: [1, 2").second != null){str} + "\n";
        var int total = 0;
        for(var int i = 0; i < 1000; i++) {
            total = total + type(YAML).Parse("k: " + i{str}).first.Get("k").first.AsInt();
        }
        s = s + total{str} + "\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, `[name, version, count, ok, none, quoted, base, derived, list, block] 10 zam 1.5 31 true true true 1 3 -3 inf [line one
line two
] false
n: plain
f: 0.1
seq:
- false
- null
- {x: 1}
|0.1
2 3 true true
499500
`, stdout)
}

// TestXML parses, reads, builds and writes XML through libxml2, escaping on
// the way out and releasing many documents under collection.
func TestXML(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("codec/format/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var XML* x = type(XML).Parse("<?xml version=\"1.0\"?>\n<root id=\"7\" lang=\"en\">\n  <item>one</item>\n  <item>two &amp; <b>bold</b></item>\n  <!-- note -->\n  <empty/>\n</root>").first;
        var str s = x.Name() + " " + x.Len(){str} + " " + x.Attrs(){str} + " " + x.Attr("id").first + " " + x.Attr("nope").second{str};
        s = s + " " + x.At(1U).Text() + " " + x.Child("empty").second{str} + " " + x.Child("zzz").second{str} + " " + x.At(1U).At(0U).Name() + "\n";

        var XML* doc = type(XML).New("doc");
        doc.SetAttr("a", "x<y & \"z\"");
        var XML* kid = doc.AddChild("kid");
        kid.SetText("1 < 2");
        doc.Append(x.At(0U));
        doc.AddChild("gone").SetAttr("k", "v");
        doc.RemoveAttr("missing");
        s = s + doc.Encode(false) + "\n" + doc.Encode(true) + "\n";
        s = s + doc.Attr("a").first + " " + kid.Text() + "\n";
        s = s + (type(XML).Parse("<a><b></a>").second != null){str} + " " + (type(XML).Parse("").second != null){str};
        var int total = 0;
        for(var int i = 0; i < 1000; i++) {
            total = total + type(Strs).ByteLen(type(XML).Parse("<n v=\"" + i{str} + "\"/>").first.Attr("v").first);
        }
        s = s + " " + total{str} + "\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, `root 3 [id, lang] 7 false two & bold true false b
<doc a="x&lt;y &amp; &quot;z&quot;"><kid>1 &lt; 2</kid><item>one</item><gone k="v"/></doc>
<doc a="x&lt;y &amp; &quot;z&quot;">
  <kid>1 &lt; 2</kid>
  <item>one</item>
  <gone k="v"/>
</doc>
x<y & "z" 1 < 2
true true 2890
`, stdout)
}

// TestBSON builds, reads, serializes and reparses BSON through libbson, in
// binary and extended JSON, and releases many documents under collection.
func TestBSON(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("lang/time/*");
import std("io/local/*");
import std("codec/format/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var BSON* b = type(BSON).New();
        b.AppendStr("name", "zam");
        b.AppendInt("n", -5);
        b.AppendFlt("f", 2.5);
        b.AppendBool("ok", true);
        b.AppendNull("none");
        var BSON* sub = type(BSON).New();
        sub.AppendInt("x", 1);
        b.AppendDoc("sub", sub);
        var BSON* arr = type(BSON).New();
        arr.AppendStr("0", "a");
        arr.AppendStr("1", "b");
        b.AppendArray("arr", arr);
        b.AppendDateTime("when", type(Time).FromUnixMilli(1700000000000));
        b.AppendObjectId("_id", "5f1d7f0e9d3e2a1b2c3d4e5f");
        var byte* raw = new(byte, 3U);
        raw[0] = 1{byte};
        raw[1] = 2{byte};
        raw[2] = 255{byte};
        b.AppendBinary("bin", raw, 3U);

        var str s = b.Keys(){str} + " " + b.Len(){str} + " " + b.GetStr("name") + " " + b.GetInt("n"){str} + " " + b.GetFlt("f"){str} + " " + b.GetInt("f"){str};
        s = s + " " + b.GetBool("ok"){str} + " " + (b.Kind("none") == type(BSON.Kind).Null){str} + " " + (b.Kind("arr") == type(BSON.Kind).Array){str} + " " + (b.Kind("zz") == type(BSON.Kind).Missing){str};
        s = s + " " + b.GetDoc("sub").GetInt("x"){str} + " " + b.GetDoc("arr").GetStr("1") + " " + b.GetDateTime("when").UnixMilli(){str} + " " + b.GetObjectId("_id") + " " + b.GetBinary("bin"){str} + "\n";
        s = s + b.ToJSON(false) + "\n";

        var Arr[byte]* bytes = b.Bytes();
        var BSON* back = type(BSON).FromBytes(bytes.Arr(), bytes.Len()).first;
        s = s + bytes.Len(){str} + " " + (back == b){str} + " " + (back == sub){str} + " " + (type(BSON).FromBytes(bytes.Arr(), 4U).second != null){str};
        var BSON* j = type(BSON).FromJSON("{\"a\": {\"$numberLong\": \"7\"}, \"b\": [1, 2]}").first;
        s = s + " " + j.GetInt("a"){str} + " " + (j.Kind("a") == type(BSON.Kind).Int64){str} + " " + j.GetDoc("b").Len(){str} + " " + j.ToJSON(true);
        s = s + " " + (type(BSON).FromJSON("{bad").second != null){str} + " " + type(Strs).ByteLen(type(BSON).NewObjectId()){str} + "\n";
        var int total = 0;
        for(var int i = 0; i < 1000; i++) {
            var BSON* t = type(BSON).New();
            t.AppendInt("v", i);
            var Arr[byte]* tb = t.Bytes();
            total = total + type(BSON).FromBytes(tb.Arr(), tb.Len()).first.GetInt("v");
        }
        s = s + total{str} + "\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, `[name, n, f, ok, none, sub, arr, when, _id, bin] 10 zam -5 2.5 2 true true true true 1 b 1700000000000 5f1d7f0e9d3e2a1b2c3d4e5f [1, 2, 255]
{ "name" : "zam", "n" : -5, "f" : 2.5, "ok" : true, "none" : null, "sub" : { "x" : 1 }, "arr" : [ "a", "b" ], "when" : { "$date" : "2023-11-14T22:13:20Z" }, "_id" : { "$oid" : "5f1d7f0e9d3e2a1b2c3d4e5f" }, "bin" : { "$binary" : { "base64" : "AQL/", "subType" : "00" } } }
145 true false true 7 true 2 { "a" : { "$numberLong" : "7" }, "b" : [ { "$numberInt" : "1" }, { "$numberInt" : "2" } ] } true 24
499500
`, stdout)
}

// TestCompression decodes streams written by the reference command-line tools,
// round-trips every codec over text and pseudo-random bytes, rejects corrupt
// input, and hands its gzip and zlib output to Go's own decoders.
func TestCompression(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("codec/text/*");
import std("codec/compress/*");

namespace Main {
    static fun bool(Arr[byte]*, byte*, uint) same = (a, b, n) {
        if(a == null || a.Len() != n) {
            return false;
        }
        var byte* got = a.Arr();
        for(var uint i = 0U; i < n; i++) {
            if(got[i] != b[i]) {
                return false;
            }
        }
        return true;
    }

    static fun Arr[byte]*(str) hex = (s) {
        return type(Hex).Decode(s).first;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str text = "zam ";
        for(var int i = 0; i < 30; i++) {
            text = text + "hello compression! ";
        }
        var Pair[byte*, int] tb = type(Strs).ToBytes(text);
        var uint tn = tb.second{uint};

        var Arr[byte]* zst = type(Main).hex("28b52ffd643e01ed0000b07a616d2068656c6c6f20636f6d7072657373696f6e2101004a58359b82650c51");
        var Arr[byte]* lz = type(Main).hex("04224d186440a723000000ff077a616d2068656c6c6f20636f6d7072657373696f6e211300ffff1250696f6e21200000000038146ba7");
        var Arr[byte]* br = type(Main).hex("a1e81100209eb254c9d124c1f5d728a42595795ec5d7e035513c3b32");
        var str s = type(Main).same(type(ZSTD).Decompress(zst.Arr(), zst.Len()).first, tb.first, tn){str};
        s = s + " " + type(Main).same(type(LZ4).Decompress(lz.Arr(), lz.Len()).first, tb.first, tn){str};
        s = s + " " + type(Main).same(type(Brotli).Decompress(br.Arr(), br.Len()).first, tb.first, tn){str} + "\n";

        var uint n = 200000U;
        var byte* noise = new(byte, n);
        var uint x = 7U;
        for(var uint i = 0U; i < n; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            noise[i] = ((x >> 59U) + 65U){byte};
        }
        var Arr[byte]* g = type(GZIP).Compress(tb.first, tn, type(GZIP).DefaultLevel);
        var Arr[byte]* z = type(ZLIB).Compress(tb.first, tn, 9);
        var Arr[byte]* l = type(LZ4).Compress(tb.first, tn, type(LZ4).DefaultLevel);
        var Arr[byte]* d = type(ZSTD).Compress(tb.first, tn, type(ZSTD).DefaultLevel);
        var Arr[byte]* b = type(Brotli).Compress(tb.first, tn, 5);
        s = s + (g.Len() < tn && z.Len() < tn && l.Len() < tn && d.Len() < tn && b.Len() < tn){str};
        s = s + " " + type(Main).same(type(GZIP).Decompress(g.Arr(), g.Len()).first, tb.first, tn){str};
        s = s + " " + type(Main).same(type(ZLIB).Decompress(z.Arr(), z.Len()).first, tb.first, tn){str};
        s = s + " " + type(Main).same(type(LZ4).Decompress(l.Arr(), l.Len()).first, tb.first, tn){str};
        s = s + " " + type(Main).same(type(ZSTD).Decompress(d.Arr(), d.Len()).first, tb.first, tn){str};
        s = s + " " + type(Main).same(type(Brotli).Decompress(b.Arr(), b.Len()).first, tb.first, tn){str};

        var Arr[byte]* gn = type(GZIP).Compress(noise, n, 1);
        var Arr[byte]* zn = type(ZLIB).Compress(noise, n, 0);
        var Arr[byte]* ln = type(LZ4).Compress(noise, n, 0);
        var Arr[byte]* dn = type(ZSTD).Compress(noise, n, -5);
        var Arr[byte]* bn = type(Brotli).Compress(noise, n, 1);
        s = s + " " + type(Main).same(type(GZIP).Decompress(gn.Arr(), gn.Len()).first, noise, n){str};
        s = s + " " + type(Main).same(type(ZLIB).Decompress(zn.Arr(), zn.Len()).first, noise, n){str};
        s = s + " " + type(Main).same(type(LZ4).Decompress(ln.Arr(), ln.Len()).first, noise, n){str};
        s = s + " " + type(Main).same(type(ZSTD).Decompress(dn.Arr(), dn.Len()).first, noise, n){str};
        s = s + " " + type(Main).same(type(Brotli).Decompress(bn.Arr(), bn.Len()).first, noise, n){str} + "\n";

        s = s + (type(GZIP).Decompress(tb.first, tn).second != null){str} + " " + (type(ZLIB).Decompress(g.Arr(), g.Len()).second != null){str};
        s = s + " " + (type(LZ4).Decompress(l.Arr(), l.Len() - 3U).second != null){str} + " " + (type(ZSTD).Decompress(d.Arr(), d.Len() - 3U).second != null){str};
        s = s + " " + (type(Brotli).Decompress(b.Arr(), b.Len() - 3U).second != null){str} + " " + (type(GZIP).Decompress(g.Arr(), 0U).second != null){str};
        s = s + " " + (type(ZSTD).Decompress(d.Arr(), 0U).second != null){str} + "\n";
        s = s + type(Hex).Encode(g.Arr(), g.Len()) + "\n" + type(Hex).Encode(z.Arr(), z.Len()) + "\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	require.Len(t, lines, 5, stdout)
	assert.Equal(t, "true true true", lines[0])
	assert.Equal(t, "true true true true true true true true true true true", lines[1])
	assert.Equal(t, "true true true true true true true", lines[2])

	want := "zam " + strings.Repeat("hello compression! ", 30)
	for i, open := range []func(io.Reader) (io.ReadCloser, error){
		func(r io.Reader) (io.ReadCloser, error) { return gzip.NewReader(r) },
		zlib.NewReader,
	} {
		raw, err := hex.DecodeString(lines[3+i])
		require.NoError(t, err)
		reader, err := open(bytes.NewReader(raw))
		require.NoError(t, err)
		got, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	}
}

// TestCBOR decodes the RFC 8949 appendix A vectors into diagnostic notation,
// encodes built values to their preferred bytes, and releases many items
// under collection.
func TestCBOR(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("codec/text/*");
import std("codec/format/*");

namespace Main {
    static fun CBOR*(str) p = (h) {
        var Arr[byte]* b = type(Hex).Decode(h).first;
        return type(CBOR).Parse(b.Arr(), b.Len()).first;
    }

    static fun str(CBOR*) hex = (c) {
        var Arr[byte]* b = c.Encode();
        return type(Hex).Encode(b.Arr(), b.Len());
    }

    fun void(uint, str...) main = (argC, argV) {
        var str s = "";
        var str* vectors = new(str, 20U);
        vectors[0] = "00";
        vectors[1] = "17";
        vectors[2] = "1818";
        vectors[3] = "1903e8";
        vectors[4] = "1bffffffffffffffff";
        vectors[5] = "3bffffffffffffffff";
        vectors[6] = "3863";
        vectors[7] = "f93c00";
        vectors[8] = "fb3ff199999999999a";
        vectors[9] = "f97c00";
        vectors[10] = "f4";
        vectors[11] = "f7";
        vectors[12] = "f6";
        vectors[13] = "c074323031332d30332d32315432303a30343a30305a";
        vectors[14] = "4401020304";
        vectors[15] = "6449455446";
        vectors[16] = "a201020304";
        vectors[17] = "7f657374726561646d696e67ff";
        vectors[18] = "9f018202039f0405ffff";
        vectors[19] = "bf61610161629f0203ffff";
        for(var uint i = 0U; i < 20U; i++) {
            s = s + type(Main).p(vectors[i]){str} + "\n";
        }
        free(vectors);

        var CBOR* m = type(Main).p("bf61610161629f0203ffff");
        s = s + type(Main).hex(m) + " " + m.Keys(){str} + " " + m.Get("b").first.At(1U).AsInt(){str} + " " + m.Get("c").second{str};
        var CBOR* pairs = type(Main).p("a201020304");
        s = s + " " + pairs.KeyAt(1U).AsInt(){str} + " " + pairs.ValueAt(1U).AsInt(){str} + " " + type(Main).p("f93c00").AsFlt(){str};
        var Pair[uint, CBOR*] tag = type(Main).p("c074323031332d30332d32315432303a30343a30305a").Tag();
        s = s + " " + tag.first{str} + " " + tag.second.AsStr() + " " + type(Main).p("7f657374726561646d696e67ff").AsStr();
        s = s + " " + (type(Main).p("4401020304").Kind() == type(CBOR.Kind).Bytes){str} + " " + type(Main).p("4401020304").AsBytes(){str} + "\n";

        var CBOR* built = type(CBOR).NewMap();
        built.Set("a", type(CBOR).NewInt(1));
        var CBOR* list = type(CBOR).NewArray();
        list.Append(type(CBOR).NewInt(2));
        built.Set("b", list);
        list.Append(type(CBOR).NewInt(3));
        s = s + type(Main).hex(built) + " " + (built == type(Main).p("a26161016162820203")){str} + " " + built{str};
        s = s + " " + type(Main).hex(type(CBOR).NewInt(-100)) + " " + type(Main).hex(type(CBOR).NewInt(1000)) + " " + type(Main).hex(type(CBOR).NewInt(-9223372036854775807 - 1));
        s = s + " " + type(Main).hex(type(CBOR).NewStr("IETF")) + " " + type(Main).hex(type(CBOR).NewTag(1U, type(CBOR).NewInt(1363896240)));
        var Arr[byte]* raw = type(Hex).Decode("01020304").first;
        s = s + " " + type(Main).hex(type(CBOR).NewBytes(raw.Arr(), raw.Len())) + " " + type(Main).hex(type(CBOR).NewFlt(1.5));
        s = s + " " + type(Main).hex(type(CBOR).Null()) + " " + type(Main).hex(type(CBOR).NewBool(true)) + "\n";

        var CBOR* parsed = type(Main).p("a26161016162820203");
        parsed.Set("a", type(CBOR).NewStr("x"));
        s = s + parsed{str};
        var Arr[byte]* trunc = type(Hex).Decode("830102").first;
        var Arr[byte]* extra = type(Hex).Decode("0000").first;
        s = s + " " + (type(CBOR).Parse(trunc.Arr(), trunc.Len()).second != null){str} + " " + (type(CBOR).Parse(extra.Arr(), extra.Len()).second != null){str};
        s = s + " " + (type(CBOR).Parse(extra.Arr(), 0U).second != null){str};
        var Arr[byte]* simple = type(Hex).Decode("f0").first;
        s = s + " " + (type(CBOR).Parse(simple.Arr(), simple.Len()).second != null){str};
        var int total = 0;
        for(var int i = 0; i < 1000; i++) {
            var CBOR* item = type(CBOR).NewArray();
            item.Append(type(CBOR).NewInt(i));
            var Arr[byte]* enc = item.Encode();
            total = total + type(CBOR).Parse(enc.Arr(), enc.Len()).first.At(0U).AsInt();
        }
        s = s + " " + total{str} + "\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, `0
23
24
1000
18446744073709551615
-18446744073709551616
-100
1.0
1.1
Infinity
false
undefined
null
0("2013-03-21T20:04:00Z")
h'01020304'
"IETF"
{1: 2, 3: 4}
"streaming"
[1, [2, 3], [4, 5]]
{"a": 1, "b": [2, 3]}
a26161016162820203 [a, b] 3 false 3 4 1 0 2013-03-21T20:04:00Z streaming true [1, 2, 3, 4]
a26161016162820203 true {"a": 1, "b": [2, 3]} 3863 1903e8 3b7fffffffffffffff 6449455446 c11a514b67b0 4401020304 fb3ff8000000000000 f6 f5
{"a": "x", "b": [2, 3]} true true true true 499500
`, stdout)
}
