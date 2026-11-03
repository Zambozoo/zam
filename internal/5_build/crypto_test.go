package build_test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCrypto runs every crypto namespace over fixed inputs, checking each
// result against Go's own implementation or a published vector, and each
// round trip, tamper and bad argument against what it should come to.
func TestCrypto(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("codec/text/*");
import std("crypto/*");
import std("crypto/hash/*");
import std("crypto/mac/*");
import std("crypto/kdf/*");
import std("crypto/cipher/*");

namespace Main {
    static fun str(Arr[byte]*) hex = (a) {
        return type(Hex).Encode(a.Arr(), a.Len());
    }

    static fun Arr[byte]*(str) unhex = (s) {
        return type(Hex).Decode(s).first;
    }

    static fun void(str, Arr[byte]*) put = (name, a) {
        type(File).StdOut.WriteStr(name + " " + type(Main).hex(a) + "\n");
    }

    static fun void(str, bool) check = (name, ok) {
        type(File).StdOut.WriteStr(name + " " + ok{str} + "\n");
    }

    static fun bool(Arr[byte]*, str) is = (a, s) {
        return type(Strs).FromBytes(a.Arr(), a.Len(){int}) == s;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str abcStr = "abc";
        var Pair[byte*, int] abc = type(Strs).ToBytes(abcStr);
        var byte* m = abc.first;
        var uint n = 3U;
        type(Main).put("md5", type(MD5).Sum(m, n));
        type(Main).put("sha1", type(SHA1).Sum(m, n));
        type(Main).put("sha224", type(SHA256).Sum224(m, n));
        type(Main).put("sha256", type(SHA256).Sum(m, n));
        type(Main).put("sha384", type(SHA512).Sum384(m, n));
        type(Main).put("sha512", type(SHA512).Sum(m, n));
        type(Main).put("sha512_256", type(SHA512).Sum256(m, n));
        type(Main).put("sha3_224", type(SHA3).Sum224(m, n));
        type(Main).put("sha3_256", type(SHA3).Sum256(m, n));
        type(Main).put("sha3_384", type(SHA3).Sum384(m, n));
        type(Main).put("sha3_512", type(SHA3).Sum512(m, n));
        type(Main).put("shake128", type(SHA3).Shake128(m, n, 40U));
        type(Main).put("shake256", type(SHA3).Shake256(m, n, 24U));
        type(Main).put("blake2b", type(BLAKE2B).Sum(m, n));
        type(Main).put("blake2s", type(BLAKE2S).Sum(m, n));
        var Digest* b2 = type(BLAKE2B).New(32U).first;
        b2.Write(m, n);
        type(Main).put("blake2b256", b2.Sum());
        type(Main).check("blake2bTooLong", type(BLAKE2B).New(65U).second != null);

        var Digest* d = type(SHA256).New();
        d.Write(m, 1U);
        d.Write(m + 1U, 2U);
        var str first = type(Main).hex(d.Sum());
        var str again = type(Main).hex(d.Sum());
        d.Reset();
        d.WriteStr("abc");
        type(Main).check("stream", first == again && type(Main).hex(d.Sum()) == first && d.Size() == 32U && d.BlockSize() == 64U);
        var Hash* h = type(SHA3).New256();
        h.Write(m, n);
        type(Main).check("hash", type(Main).hex(h.Sum()) == type(Main).hex(type(SHA3).Sum256(m, n)) && h.Size() == 32U);

        var str keyStr = "key";
        var Pair[byte*, int] key = type(Strs).ToBytes(keyStr);
        var str foxStr = "The quick brown fox jumps over the lazy dog";
        var Pair[byte*, int] fox = type(Strs).ToBytes(foxStr);
        var Arr[byte]* tag = type(HMAC).Of(type(Digest.Kind).SHA256, key.first, 3U, fox.first, fox.second{uint}).first;
        type(Main).put("hmac256", tag);
        var HMAC* mac = type(HMAC).New(type(Digest.Kind).SHA512, key.first, 3U).first;
        mac.WriteStr(foxStr);
        var str macFirst = type(Main).hex(mac.Sum());
        type(File).StdOut.WriteStr("hmac512 " + macFirst + "\n");
        mac.Reset();
        mac.Write(fox.first, fox.second{uint});
        type(Main).check("hmacReset", type(Main).hex(mac.Sum()) == macFirst && mac.Size() == 64U && mac.BlockSize() == 128U);
        type(Main).check("hmacVerify",
            type(HMAC).Verify(type(Digest.Kind).SHA256, key.first, 3U, fox.first, fox.second{uint}, tag.Arr(), tag.Len()) &&
            !type(HMAC).Verify(type(Digest.Kind).SHA256, key.first, 3U, fox.first, fox.second{uint} - 1U, tag.Arr(), tag.Len()) &&
            !type(HMAC).Verify(type(Digest.Kind).SHA256, key.first, 3U, fox.first, fox.second{uint}, tag.Arr(), 31U));

        var Arr[byte]* polyKey = type(Main).unhex("85d6be7857556d337f4452fe42d506a80103808afb0db2fd4abff6af4149f51b");
        var str polyStr = "Cryptographic Forum Research Group";
        var Pair[byte*, int] polyMsg = type(Strs).ToBytes(polyStr);
        var Arr[byte]* polyTag = type(Poly1305).Sum(polyKey.Arr(), 32U, polyMsg.first, polyMsg.second{uint}).first;
        type(Main).put("poly1305", polyTag);
        type(Main).check("poly1305Verify",
            type(Poly1305).Verify(polyKey.Arr(), 32U, polyMsg.first, polyMsg.second{uint}, polyTag.Arr(), 16U) &&
            !type(Poly1305).Verify(polyKey.Arr(), 32U, polyMsg.first, 3U, polyTag.Arr(), 16U) &&
            type(Poly1305).Sum(polyKey.Arr(), 16U, polyMsg.first, 3U).second != null);

        var str secretStr = "secret";
        var Pair[byte*, int] secret = type(Strs).ToBytes(secretStr);
        var str saltStr = "salt";
        var Pair[byte*, int] salt = type(Strs).ToBytes(saltStr);
        var str infoStr = "info";
        var Pair[byte*, int] info = type(Strs).ToBytes(infoStr);
        var Arr[byte]* okm = type(HKDF).Key(type(Digest.Kind).SHA256, secret.first, 6U, salt.first, 4U, info.first, 4U, 42U).first;
        type(Main).put("hkdf", okm);
        var Arr[byte]* prk = type(HKDF).Extract(type(Digest.Kind).SHA256, secret.first, 6U, salt.first, 4U).first;
        var Arr[byte]* expanded = type(HKDF).Expand(type(Digest.Kind).SHA256, prk.Arr(), prk.Len(), info.first, 4U, 42U).first;
        type(Main).check("hkdfParts", type(Main).hex(expanded) == type(Main).hex(okm));
        var str passwordStr = "password";
        var Pair[byte*, int] password = type(Strs).ToBytes(passwordStr);
        type(Main).put("pbkdf2", type(PBKDF2).Key(type(Digest.Kind).SHA256, password.first, 8U, salt.first, 4U, 4096U, 32U).first);
        var str naclStr = "NaCl";
        var Pair[byte*, int] nacl = type(Strs).ToBytes(naclStr);
        type(Main).put("scrypt", type(Scrypt).Key(password.first, 8U, nacl.first, 4U, 1024U, 8U, 16U, 64U).first);
        type(Main).check("scryptBad", type(Scrypt).Key(password.first, 8U, nacl.first, 4U, 1000U, 8U, 16U, 64U).second != null);
        var str someSaltStr = "somesalt0";
        var Pair[byte*, int] someSalt = type(Strs).ToBytes(someSaltStr);
        type(Main).put("argon2", type(Argon2).Key(password.first, 8U, someSalt.first, 9U, 2U, 1024U, 2U, 32U).first);

        var str hunterStr = "hunter2";
        var Pair[byte*, int] hunter = type(Strs).ToBytes(hunterStr);
        var str wrongStr = "hunter3";
        var Pair[byte*, int] wrong = type(Strs).ToBytes(wrongStr);
        var str argonHash = type(Argon2).Hash(hunter.first, 7U, 2U, 1024U).first;
        type(Main).check("argon2Hash", type(Strs).Split(argonHash, '$'{byte}).Get(1U) == "argon2id" &&
            type(Argon2).Verify(argonHash, hunter.first, 7U) && !type(Argon2).Verify(argonHash, wrong.first, 7U) &&
            !type(Argon2).Verify("nonsense", hunter.first, 7U));
        var str bcryptHash = type(BCrypt).Hash(hunter.first, 7U, 4).first;
        var str horseStr = "correct horse";
        var Pair[byte*, int] horse = type(Strs).ToBytes(horseStr);
        var str longStr = "0123456789012345678901234567890123456789012345678901234567890123456789012";
        var Pair[byte*, int] long = type(Strs).ToBytes(longStr);
        type(Main).check("bcrypt", type(BCrypt).Verify(bcryptHash, hunter.first, 7U) &&
            !type(BCrypt).Verify(bcryptHash, wrong.first, 7U) &&
            type(BCrypt).Verify("$2y$05$MvkiluqVjxioMVZkrMGUiOS/ewMzshqFuQ6Oua33CZXDRyz9W6FJO", horse.first, 13U) &&
            !type(BCrypt).Verify("$2y$05$MvkiluqVjxioMVZkrMGUiOS/ewMzshqFuQ6Oua33CZXDRyz9W6FJO", hunter.first, 7U) &&
            type(BCrypt).Hash(hunter.first, 7U, 3).second != null &&
            type(BCrypt).Hash(long.first, 73U, 4).second != null);

        var Arr[byte]* key16 = type(Main).unhex("000102030405060708090a0b0c0d0e0f");
        var Arr[byte]* iv = type(Main).unhex("101112131415161718191a1b1c1d1e1f");
        var Arr[byte]* nonce12 = type(Main).unhex("202122232425262728292a2b");
        var str aadStr = "header";
        var Pair[byte*, int] aad = type(Strs).ToBytes(aadStr);
        var AEAD* gcm = type(AES).GCM(key16.Arr(), 16U).first;
        var Arr[byte]* sealed = gcm.Seal(nonce12.Arr(), fox.first, fox.second{uint}, aad.first, 6U);
        type(Main).put("gcm", sealed);
        var Arr[byte]* opened = gcm.Open(nonce12.Arr(), sealed.Arr(), sealed.Len(), aad.first, 6U).first;
        var bool aadWrong = gcm.Open(nonce12.Arr(), sealed.Arr(), sealed.Len(), aad.first, 5U).second != null;
        sealed.Set(0U, (sealed.Get(0U){uint} + 1U){byte});
        type(Main).check("gcmOpen", type(Main).is(opened, foxStr) && aadWrong &&
            gcm.Open(nonce12.Arr(), sealed.Arr(), sealed.Len(), aad.first, 6U).second != null &&
            gcm.Open(nonce12.Arr(), sealed.Arr(), 15U, aad.first, 6U).second != null &&
            type(AES).GCM(key16.Arr(), 15U).second != null && gcm.NonceSize() == 12U && gcm.Overhead() == 16U);
        var Arr[byte]* cbc = type(AES).EncryptCBC(key16.Arr(), 16U, iv.Arr(), fox.first, fox.second{uint}).first;
        type(Main).put("cbc", cbc);
        type(Main).check("cbcOpen", type(Main).is(type(AES).DecryptCBC(key16.Arr(), 16U, iv.Arr(), cbc.Arr(), cbc.Len()).first, foxStr) &&
            type(AES).DecryptCBC(key16.Arr(), 16U, iv.Arr(), cbc.Arr(), 20U).second != null &&
            type(AES).EncryptCBC(key16.Arr(), 12U, iv.Arr(), fox.first, 3U).second != null);
        var Arr[byte]* ctr = type(AES).CTR(key16.Arr(), 16U, iv.Arr(), fox.first, fox.second{uint}).first;
        type(Main).put("ctr", ctr);
        type(Main).check("ctrOpen", type(Main).is(type(AES).CTR(key16.Arr(), 16U, iv.Arr(), ctr.Arr(), ctr.Len()).first, foxStr));

        var Arr[byte]* chachaKey = type(Main).unhex("808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f");
        var Arr[byte]* chachaNonce = type(Main).unhex("070000004041424344454647");
        var Arr[byte]* chachaAad = type(Main).unhex("50515253c0c1c2c3c4c5c6c7");
        var str sunStr = "Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.";
        var Pair[byte*, int] sun = type(Strs).ToBytes(sunStr);
        var AEAD* chacha = type(ChaCha20Poly1305).New(chachaKey.Arr(), 32U).first;
        var Arr[byte]* chachaSealed = chacha.Seal(chachaNonce.Arr(), sun.first, sun.second{uint}, chachaAad.Arr(), 12U);
        type(Main).put("chacha", chachaSealed);
        type(Main).check("chachaOpen", type(Main).is(chacha.Open(chachaNonce.Arr(), chachaSealed.Arr(), chachaSealed.Len(), chachaAad.Arr(), 12U).first, sunStr) &&
            chacha.Open(chachaNonce.Arr(), chachaSealed.Arr(), chachaSealed.Len(), chachaAad.Arr(), 11U).second != null &&
            type(ChaCha20Poly1305).New(chachaKey.Arr(), 16U).second != null);
        var AEAD* xchacha = type(ChaCha20Poly1305).NewX(chachaKey.Arr(), 32U).first;
        var Arr[byte]* nonce24 = type(Rand).Bytes(24U);
        var Arr[byte]* xSealed = xchacha.Seal(nonce24.Arr(), sun.first, sun.second{uint}, chachaAad.Arr(), 12U);
        type(Main).check("xchacha", xSealed.Len() == sun.second{uint} + 16U && xchacha.NonceSize() == 24U &&
            type(Main).is(xchacha.Open(nonce24.Arr(), xSealed.Arr(), xSealed.Len(), chachaAad.Arr(), 12U).first, sunStr) &&
            xchacha.Open(nonce24.Arr(), xSealed.Arr(), xSealed.Len(), null, 0U).second != null);

        var AEAD* box = type(Salsa20).SecretBox(chachaKey.Arr(), 32U).first;
        var uint foxLen = fox.second{uint};
        var Arr[byte]* boxed = box.Seal(nonce24.Arr(), fox.first, foxLen, null, 0U);
        var byte* padded = new(byte, 32U + foxLen);
        type(Arrs)[byte].Copy(foxLen, fox.first, padded + 32U);
        var Arr[byte]* stream = type(Salsa20).XOR(chachaKey.Arr(), 32U, nonce24.Arr(), 24U, 0U, padded, 32U + foxLen).first;
        free(padded);
        type(Main).check("secretbox", boxed.Len() == foxLen + 16U &&
            type(Subtle).Equal(boxed.Arr() + 16U, stream.Arr() + 32U, foxLen) &&
            type(Poly1305).Verify(stream.Arr(), 32U, stream.Arr() + 32U, foxLen, boxed.Arr(), 16U) &&
            type(Main).is(box.Open(nonce24.Arr(), boxed.Arr(), boxed.Len(), null, 0U).first, foxStr) &&
            box.Open(nonce24.Arr(), boxed.Arr(), boxed.Len() - 1U, null, 0U).second != null);
        var byte* zeros = new(byte, 128U);
        var Arr[byte]* whole = type(Salsa20).XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 8U, 0U, zeros, 128U).first;
        var Arr[byte]* second = type(Salsa20).XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 8U, 1U, zeros, 64U).first;
        free(zeros);
        var Arr[byte]* there = type(Salsa20).XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 8U, 0U, fox.first, foxLen).first;
        type(Main).check("salsa20", type(Subtle).Equal(whole.Arr() + 64U, second.Arr(), 64U) &&
            type(Main).is(type(Salsa20).XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 8U, 0U, there.Arr(), foxLen).first, foxStr) &&
            type(Salsa20).XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 12U, 0U, fox.first, foxLen).second != null);

        var bool below = true;
        for(var int i = 0; i < 100; i++) {
            below = below && type(Rand).UintN(10U) < 10U;
        }
        var Arr[byte]* r1 = type(Rand).Bytes(32U);
        var Arr[byte]* r2 = type(Rand).Bytes(32U);
        type(Main).check("rand", below && r1.Len() == 32U && type(Main).hex(r1) != type(Main).hex(r2));
        type(Main).check("subtle", type(Subtle).EqualStr("abc", "abc") && !type(Subtle).EqualStr("abc", "abd") &&
            !type(Subtle).EqualStr("ab", "abc") && type(Subtle).Equal(m, m, 0U));
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)

	got := map[string]string{}
	for line := range strings.Lines(stdout) {
		label, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		got[label] = value
	}

	abc := []byte("abc")
	fox := []byte("The quick brown fox jumps over the lazy dog")
	h := func(b []byte) string { return hex.EncodeToString(b) }
	mac := func(newHash func() hash.Hash, key, msg []byte) string {
		m := hmac.New(newHash, key)
		m.Write(msg)
		return h(m.Sum(nil))
	}
	key16, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	iv, _ := hex.DecodeString("101112131415161718191a1b1c1d1e1f")
	nonce12, _ := hex.DecodeString("202122232425262728292a2b")
	block, err := aes.NewCipher(key16)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	pad := 16 - len(fox)%16
	padded := append([]byte(string(fox)), []byte(strings.Repeat(string(rune(pad)), pad))...)
	cbc := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(cbc, padded)
	ctr := make([]byte, len(fox))
	cipher.NewCTR(block, iv).XORKeyStream(ctr, fox)
	okm, err := hkdf.Key(sha256.New, []byte("secret"), []byte("salt"), "info", 42)
	require.NoError(t, err)
	dk, err := pbkdf2.Key(sha256.New, "password", []byte("salt"), 4096, 32)
	require.NoError(t, err)

	md5Sum, sha1Sum, sha224, sha256Sum := md5.Sum(abc), sha1.Sum(abc), sha256.Sum224(abc), sha256.Sum256(abc)
	sha384, sha512Sum, sha512256 := sha512.Sum384(abc), sha512.Sum512(abc), sha512.Sum512_256(abc)
	s3224, s3256, s3384, s3512 := sha3.Sum224(abc), sha3.Sum256(abc), sha3.Sum384(abc), sha3.Sum512(abc)
	want := map[string]string{
		"md5":        h(md5Sum[:]),
		"sha1":       h(sha1Sum[:]),
		"sha224":     h(sha224[:]),
		"sha256":     h(sha256Sum[:]),
		"sha384":     h(sha384[:]),
		"sha512":     h(sha512Sum[:]),
		"sha512_256": h(sha512256[:]),
		"sha3_224":   h(s3224[:]),
		"sha3_256":   h(s3256[:]),
		"sha3_384":   h(s3384[:]),
		"sha3_512":   h(s3512[:]),
		"shake128":   h(sha3.SumSHAKE128(abc, 40)),
		"shake256":   h(sha3.SumSHAKE256(abc, 24)),
		"blake2b":    "ba80a53f981c4d0d6a2797b69f12f6e94c212f14685ac4b74b12bb6fdbffa2d17d87c5392aab792dc252d5de4533cc9518d38aa8dbf1925ab92386edd4009923",
		"blake2b256": "bddd813c634239723171ef3fee98579b94964e3bb1cb3e427262c8c068d52319",
		"blake2s":    "508c5e8c327c14e2e1a72ba34eeb452f37458b209ed63a294d999b4c86675982",
		"hmac256":    mac(sha256.New, []byte("key"), fox),
		"hmac512":    mac(sha512.New, []byte("key"), fox),
		"poly1305":   "a8061dc1305136c6c22b8baf0c0127a9",
		"hkdf":       h(okm),
		"pbkdf2":     h(dk),
		"scrypt":     "fdbabe1c9d3472007856e7190d01e9fe7c6ad7cbc8237830e77376634b3731622eaf30d92e22a3886ff109279d9830dac727afb94a83ee6d8360cbdfa2cc0640",
		"argon2":     "0de3540d85254a30cdb7bb5818d33aae6522a5a3b834fef059483598a0b9b381",
		"gcm":        h(gcm.Seal(nil, nonce12, fox, []byte("header"))),
		"cbc":        h(cbc),
		"ctr":        h(ctr),
	}
	for label, value := range want {
		assert.Equal(t, value, got[label], label)
	}
	for _, label := range []string{
		"blake2bTooLong", "stream", "hash", "hmacReset", "hmacVerify", "poly1305Verify", "hkdfParts",
		"scryptBad", "argon2Hash", "bcrypt", "gcmOpen", "cbcOpen", "ctrOpen", "chachaOpen", "xchacha",
		"secretbox", "salsa20", "rand", "subtle",
	} {
		assert.Equal(t, "true", got[label], label)
	}
	chacha := got["chacha"]
	assert.True(t, strings.HasPrefix(chacha, "d31a8d34648e60db7b86afbc53ef7ec2"), chacha)
	assert.True(t, strings.HasSuffix(chacha, "1ae10b594f09e26a7e902ecbd0600691"), chacha)
}
