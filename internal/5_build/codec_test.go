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
        var str s = "[" + Base64::EncodeStr("") + "] " + Base64::EncodeStr("f") + " " + Base64::EncodeStr("fo") + " " + Base64::EncodeStr("foo");
        s = s + " " + Base64::EncodeStr("foob") + " " + Base64::EncodeStr("fooba") + " " + Base64::EncodeStr("foobar");
        s = s + " " + Base64::DecodeStr("Zm9vYmE=").0 + " " + Base64::DecodeStr("Zm9vYmE").0 + " " + Base64::DecodeStr("Zm9vYmFy").0;
        s = s + " " + Base64::DecodeStr("Zm9*").1.Err() + " " + Base64::DecodeStr("Zm9vY").1.Err();
        var byte* b = new(byte, 2U);
        b[0] = 251{byte};
        b[1] = 255{byte};
        var str u = Base64::EncodeURL(b, 2U);
        var Arr[byte]* back = Base64::DecodeURL(u).0;
        s = s + " " + u + " " + Base64::Encode(b, 2U) + " " + back.Get(0U){int}{str} + "," + back.Get(1U){int}{str} + "\n";

        s = s + Hex::EncodeStr("hi!") + " " + Hex::DecodeStr("48656C6c6F").0 + " " + Hex::Encode(b, 2U);
        s = s + " " + Hex::DecodeStr("abc").1.Err() + " " + Hex::DecodeStr("zz").1.Err() + " [" + Hex::EncodeStr("") + "]\n";
        File::StdOut.WriteStr(s);
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
        var JSON* doc = JSON::Parse("{\"name\": \"zam\", \"tags\": [\"a\", \"b\"], \"n\": 3, \"pi\": 3.5, \"ok\": true, \"none\": null, \"nested\": {\"x\": -1}}").0;
        var str s = doc.Keys(){str} + " " + doc.Len(){str} + " " + doc.Get("name").0.AsStr() + " " + doc.Get("tags").0.At(1U).AsStr();
        s = s + " " + doc.Get("n").0.AsInt(){str} + " " + doc.Get("pi").0.AsFlt(){str} + " " + doc.Get("pi").0.AsInt(){str} + " " + doc.Get("ok").0.AsBool(){str};
        s = s + " " + doc.Get("none").1{str} + " " + doc.Get("none").0.IsNull(){str} + " " + doc.Get("missing").1{str};
        s = s + " " + (doc.Get("nested").0.Kind() == JSON.Kind::Object){str} + " " + (doc.Get("n").0.Kind() == JSON.Kind::Int){str} + "\n";

        doc.Get("nested").0.Set("y", JSON::NewStr("q\"/"));
        doc.Remove("tags");
        doc.Remove("pi");
        s = s + doc{str} + "\n";

        var JSON* built = JSON::NewObject();
        var JSON* list = JSON::NewArray();
        list.Append(JSON::NewBool(false));
        list.Append(null);
        list.Append(JSON::NewFlt(0.25));
        list.SetAt(0U, JSON::NewInt(-7));
        built.Set("list", list);
        built.Set("empty", JSON::NewObject());
        s = s + built.Encode(false) + " " + list.Len(){str} + " " + JSON::Null(){str} + "\n";

        s = s + (JSON::Parse("{\"a\": [1, 2]}").0 == JSON::Parse(" { \"a\" : [ 1 , 2 ] } ").0){str};
        s = s + " " + (JSON::Parse("[1]").0 == JSON::Parse("[2]").0){str};
        s = s + " " + (JSON::Parse("{\"a\":}").1 != null){str} + " " + (JSON::Parse("1 2").1 != null){str};
        s = s + " " + JSON::Parse("  42 ").0.AsInt(){str} + " " + JSON::Parse("\"x\"").0{str};
        var int total = 0;
        for(var int i = 0; i < 2000; i++) {
            total = total + JSON::Parse("[" + i{str} + ", {\"k\": \"v\"}]").0.At(0U).AsInt();
        }
        s = s + " " + total{str} + "\n";
        File::StdOut.WriteStr(s);
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
        var YAML* y = YAML::Parse(src).0;
        var str s = y.Keys(){str} + " " + y.Len(){str} + " " + y.Get("name").0.AsStr() + " " + y.Get("version").0.AsFlt(){str};
        s = s + " " + y.Get("count").0.AsInt(){str} + " " + y.Get("ok").0.AsBool(){str} + " " + y.Get("none").0.IsNull(){str};
        s = s + " " + (y.Get("quoted").0.Kind() == YAML.Kind::String){str} + " " + y.Get("derived").0.Get("x").0.AsInt(){str};
        s = s + " " + y.Get("list").0.Len(){str} + " " + y.Get("list").0.At(1U).AsInt(){str} + " " + y.Get("list").0.At(2U).AsFlt(){str};
        s = s + " [" + y.Get("block").0.AsStr() + "] " + y.Get("missing").1{str} + "\n";

        var YAML* m = YAML::NewMap();
        m.Set("s", YAML::NewStr("true"));
        m.Set("n", YAML::NewInt(42));
        m.Set("f", YAML::NewFlt(0.1));
        var YAML* seq = YAML::NewSeq();
        seq.Append(YAML::NewBool(false));
        seq.Append(null);
        seq.Append(y.Get("base").0);
        m.Set("seq", seq);
        m.Set("n", YAML::NewStr("plain"));
        m.Remove("s");
        s = s + m.Encode() + "|" + YAML::Parse(m.Encode()).0.Get("f").0.AsFlt(){str} + "\n";

        var (Arr[YAML*]*, Err*) all = YAML::ParseAll("--- 1\n--- [2, 3]\n");
        s = s + all.0.Len(){str} + " " + all.0.Get(1U).At(1U).AsInt(){str} + " " + YAML::Parse("").0.IsNull(){str};
        s = s + " " + (YAML::Parse("a: [1, 2").1 != null){str} + "\n";
        var int total = 0;
        for(var int i = 0; i < 1000; i++) {
            total = total + YAML::Parse("k: " + i{str}).0.Get("k").0.AsInt();
        }
        s = s + total{str} + "\n";
        File::StdOut.WriteStr(s);
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
        var XML* x = XML::Parse("<?xml version=\"1.0\"?>\n<root id=\"7\" lang=\"en\">\n  <item>one</item>\n  <item>two &amp; <b>bold</b></item>\n  <!-- note -->\n  <empty/>\n</root>").0;
        var str s = x.Name() + " " + x.Len(){str} + " " + x.Attrs(){str} + " " + x.Attr("id").0 + " " + x.Attr("nope").1{str};
        s = s + " " + x.At(1U).Text() + " " + x.Child("empty").1{str} + " " + x.Child("zzz").1{str} + " " + x.At(1U).At(0U).Name() + "\n";

        var XML* doc = XML::New("doc");
        doc.SetAttr("a", "x<y & \"z\"");
        var XML* kid = doc.AddChild("kid");
        kid.SetText("1 < 2");
        doc.Append(x.At(0U));
        doc.AddChild("gone").SetAttr("k", "v");
        doc.RemoveAttr("missing");
        s = s + doc.Encode(false) + "\n" + doc.Encode(true) + "\n";
        s = s + doc.Attr("a").0 + " " + kid.Text() + "\n";
        s = s + (XML::Parse("<a><b></a>").1 != null){str} + " " + (XML::Parse("").1 != null){str};
        var int total = 0;
        for(var int i = 0; i < 1000; i++) {
            total = total + Strs::ByteLen(XML::Parse("<n v=\"" + i{str} + "\"/>").0.Attr("v").0);
        }
        s = s + " " + total{str} + "\n";
        File::StdOut.WriteStr(s);
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
        var BSON* b = BSON::New();
        b.AppendStr("name", "zam");
        b.AppendInt("n", -5);
        b.AppendFlt("f", 2.5);
        b.AppendBool("ok", true);
        b.AppendNull("none");
        var BSON* sub = BSON::New();
        sub.AppendInt("x", 1);
        b.AppendDoc("sub", sub);
        var BSON* arr = BSON::New();
        arr.AppendStr("0", "a");
        arr.AppendStr("1", "b");
        b.AppendArray("arr", arr);
        b.AppendDateTime("when", Time::FromUnixMilli(1700000000000));
        b.AppendObjectId("_id", "5f1d7f0e9d3e2a1b2c3d4e5f");
        var byte* raw = new(byte, 3U);
        raw[0] = 1{byte};
        raw[1] = 2{byte};
        raw[2] = 255{byte};
        b.AppendBinary("bin", raw, 3U);

        var str s = b.Keys(){str} + " " + b.Len(){str} + " " + b.GetStr("name") + " " + b.GetInt("n"){str} + " " + b.GetFlt("f"){str} + " " + b.GetInt("f"){str};
        s = s + " " + b.GetBool("ok"){str} + " " + (b.Kind("none") == BSON.Kind::Null){str} + " " + (b.Kind("arr") == BSON.Kind::Array){str} + " " + (b.Kind("zz") == BSON.Kind::Missing){str};
        s = s + " " + b.GetDoc("sub").GetInt("x"){str} + " " + b.GetDoc("arr").GetStr("1") + " " + b.GetDateTime("when").UnixMilli(){str} + " " + b.GetObjectId("_id") + " " + b.GetBinary("bin"){str} + "\n";
        s = s + b.ToJSON(false) + "\n";

        var Arr[byte]* bytes = b.Bytes();
        var BSON* back = BSON::FromBytes(bytes.Arr(), bytes.Len()).0;
        s = s + bytes.Len(){str} + " " + (back == b){str} + " " + (back == sub){str} + " " + (BSON::FromBytes(bytes.Arr(), 4U).1 != null){str};
        var BSON* j = BSON::FromJSON("{\"a\": {\"$numberLong\": \"7\"}, \"b\": [1, 2]}").0;
        s = s + " " + j.GetInt("a"){str} + " " + (j.Kind("a") == BSON.Kind::Int64){str} + " " + j.GetDoc("b").Len(){str} + " " + j.ToJSON(true);
        s = s + " " + (BSON::FromJSON("{bad").1 != null){str} + " " + Strs::ByteLen(BSON::NewObjectId()){str} + "\n";
        var int total = 0;
        for(var int i = 0; i < 1000; i++) {
            var BSON* t = BSON::New();
            t.AppendInt("v", i);
            var Arr[byte]* tb = t.Bytes();
            total = total + BSON::FromBytes(tb.Arr(), tb.Len()).0.GetInt("v");
        }
        s = s + total{str} + "\n";
        File::StdOut.WriteStr(s);
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
        return Hex::Decode(s).0;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str text = "zam ";
        for(var int i = 0; i < 30; i++) {
            text = text + "hello compression! ";
        }
        var (byte*, int) tb = Strs::ToBytes(text);
        var uint tn = tb.1{uint};

        var Arr[byte]* zst = Main::hex("28b52ffd643e01ed0000b07a616d2068656c6c6f20636f6d7072657373696f6e2101004a58359b82650c51");
        var Arr[byte]* lz = Main::hex("04224d186440a723000000ff077a616d2068656c6c6f20636f6d7072657373696f6e211300ffff1250696f6e21200000000038146ba7");
        var Arr[byte]* br = Main::hex("a1e81100209eb254c9d124c1f5d728a42595795ec5d7e035513c3b32");
        var str s = Main::same(ZSTD::Decompress(zst.Arr(), zst.Len()).0, tb.0, tn){str};
        s = s + " " + Main::same(LZ4::Decompress(lz.Arr(), lz.Len()).0, tb.0, tn){str};
        s = s + " " + Main::same(Brotli::Decompress(br.Arr(), br.Len()).0, tb.0, tn){str} + "\n";

        var uint n = 200000U;
        var byte* noise = new(byte, n);
        var uint x = 7U;
        for(var uint i = 0U; i < n; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            noise[i] = ((x >> 59U) + 65U){byte};
        }
        var Arr[byte]* g = GZIP::Compress(tb.0, tn, GZIP::DefaultLevel);
        var Arr[byte]* z = ZLIB::Compress(tb.0, tn, 9);
        var Arr[byte]* l = LZ4::Compress(tb.0, tn, LZ4::DefaultLevel);
        var Arr[byte]* d = ZSTD::Compress(tb.0, tn, ZSTD::DefaultLevel);
        var Arr[byte]* b = Brotli::Compress(tb.0, tn, 5);
        s = s + (g.Len() < tn && z.Len() < tn && l.Len() < tn && d.Len() < tn && b.Len() < tn){str};
        s = s + " " + Main::same(GZIP::Decompress(g.Arr(), g.Len()).0, tb.0, tn){str};
        s = s + " " + Main::same(ZLIB::Decompress(z.Arr(), z.Len()).0, tb.0, tn){str};
        s = s + " " + Main::same(LZ4::Decompress(l.Arr(), l.Len()).0, tb.0, tn){str};
        s = s + " " + Main::same(ZSTD::Decompress(d.Arr(), d.Len()).0, tb.0, tn){str};
        s = s + " " + Main::same(Brotli::Decompress(b.Arr(), b.Len()).0, tb.0, tn){str};

        var Arr[byte]* gn = GZIP::Compress(noise, n, 1);
        var Arr[byte]* zn = ZLIB::Compress(noise, n, 0);
        var Arr[byte]* ln = LZ4::Compress(noise, n, 0);
        var Arr[byte]* dn = ZSTD::Compress(noise, n, -5);
        var Arr[byte]* bn = Brotli::Compress(noise, n, 1);
        s = s + " " + Main::same(GZIP::Decompress(gn.Arr(), gn.Len()).0, noise, n){str};
        s = s + " " + Main::same(ZLIB::Decompress(zn.Arr(), zn.Len()).0, noise, n){str};
        s = s + " " + Main::same(LZ4::Decompress(ln.Arr(), ln.Len()).0, noise, n){str};
        s = s + " " + Main::same(ZSTD::Decompress(dn.Arr(), dn.Len()).0, noise, n){str};
        s = s + " " + Main::same(Brotli::Decompress(bn.Arr(), bn.Len()).0, noise, n){str} + "\n";

        s = s + (GZIP::Decompress(tb.0, tn).1 != null){str} + " " + (ZLIB::Decompress(g.Arr(), g.Len()).1 != null){str};
        s = s + " " + (LZ4::Decompress(l.Arr(), l.Len() - 3U).1 != null){str} + " " + (ZSTD::Decompress(d.Arr(), d.Len() - 3U).1 != null){str};
        s = s + " " + (Brotli::Decompress(b.Arr(), b.Len() - 3U).1 != null){str} + " " + (GZIP::Decompress(g.Arr(), 0U).1 != null){str};
        s = s + " " + (ZSTD::Decompress(d.Arr(), 0U).1 != null){str} + "\n";
        s = s + Hex::Encode(g.Arr(), g.Len()) + "\n" + Hex::Encode(z.Arr(), z.Len()) + "\n";
        File::StdOut.WriteStr(s);
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
        var Arr[byte]* b = Hex::Decode(h).0;
        return CBOR::Parse(b.Arr(), b.Len()).0;
    }

    static fun str(CBOR*) hex = (c) {
        var Arr[byte]* b = c.Encode();
        return Hex::Encode(b.Arr(), b.Len());
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
            s = s + Main::p(vectors[i]){str} + "\n";
        }
        free(vectors);

        var CBOR* m = Main::p("bf61610161629f0203ffff");
        s = s + Main::hex(m) + " " + m.Keys(){str} + " " + m.Get("b").0.At(1U).AsInt(){str} + " " + m.Get("c").1{str};
        var CBOR* pairs = Main::p("a201020304");
        s = s + " " + pairs.KeyAt(1U).AsInt(){str} + " " + pairs.ValueAt(1U).AsInt(){str} + " " + Main::p("f93c00").AsFlt(){str};
        var (uint, CBOR*) tag = Main::p("c074323031332d30332d32315432303a30343a30305a").Tag();
        s = s + " " + tag.0{str} + " " + tag.1.AsStr() + " " + Main::p("7f657374726561646d696e67ff").AsStr();
        s = s + " " + (Main::p("4401020304").Kind() == CBOR.Kind::Bytes){str} + " " + Main::p("4401020304").AsBytes(){str} + "\n";

        var CBOR* built = CBOR::NewMap();
        built.Set("a", CBOR::NewInt(1));
        var CBOR* list = CBOR::NewArray();
        list.Append(CBOR::NewInt(2));
        built.Set("b", list);
        list.Append(CBOR::NewInt(3));
        s = s + Main::hex(built) + " " + (built == Main::p("a26161016162820203")){str} + " " + built{str};
        s = s + " " + Main::hex(CBOR::NewInt(-100)) + " " + Main::hex(CBOR::NewInt(1000)) + " " + Main::hex(CBOR::NewInt(-9223372036854775807 - 1));
        s = s + " " + Main::hex(CBOR::NewStr("IETF")) + " " + Main::hex(CBOR::NewTag(1U, CBOR::NewInt(1363896240)));
        var Arr[byte]* raw = Hex::Decode("01020304").0;
        s = s + " " + Main::hex(CBOR::NewBytes(raw.Arr(), raw.Len())) + " " + Main::hex(CBOR::NewFlt(1.5));
        s = s + " " + Main::hex(CBOR::Null()) + " " + Main::hex(CBOR::NewBool(true)) + "\n";

        var CBOR* parsed = Main::p("a26161016162820203");
        parsed.Set("a", CBOR::NewStr("x"));
        s = s + parsed{str};
        var Arr[byte]* trunc = Hex::Decode("830102").0;
        var Arr[byte]* extra = Hex::Decode("0000").0;
        s = s + " " + (CBOR::Parse(trunc.Arr(), trunc.Len()).1 != null){str} + " " + (CBOR::Parse(extra.Arr(), extra.Len()).1 != null){str};
        s = s + " " + (CBOR::Parse(extra.Arr(), 0U).1 != null){str};
        var Arr[byte]* simple = Hex::Decode("f0").0;
        s = s + " " + (CBOR::Parse(simple.Arr(), simple.Len()).1 != null){str};
        var int total = 0;
        for(var int i = 0; i < 1000; i++) {
            var CBOR* item = CBOR::NewArray();
            item.Append(CBOR::NewInt(i));
            var Arr[byte]* enc = item.Encode();
            total = total + CBOR::Parse(enc.Arr(), enc.Len()).0.At(0U).AsInt();
        }
        s = s + " " + total{str} + "\n";
        File::StdOut.WriteStr(s);
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
