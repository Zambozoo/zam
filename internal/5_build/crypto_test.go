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
        return Hex::Encode(a.Arr(), a.Len());
    }

    static fun Arr[byte]*(str) unhex = (s) {
        return Hex::Decode(s).0;
    }

    static fun void(str, Arr[byte]*) put = (name, a) {
        File::StdOut.WriteStr(name + " " + Main::hex(a) + "\n");
    }

    static fun void(str, bool) check = (name, ok) {
        File::StdOut.WriteStr(name + " " + ok{str} + "\n");
    }

    static fun bool(Arr[byte]*, str) is = (a, s) {
        return Strs::FromBytes(a.Arr(), a.Len(){int}) == s;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str abcStr = "abc";
        var (byte*, int) abc = Strs::ToBytes(abcStr);
        var byte* m = abc.0;
        var uint n = 3U;
        Main::put("md5", MD5::Sum(m, n));
        Main::put("sha1", SHA1::Sum(m, n));
        Main::put("sha224", SHA256::Sum224(m, n));
        Main::put("sha256", SHA256::Sum(m, n));
        Main::put("sha384", SHA512::Sum384(m, n));
        Main::put("sha512", SHA512::Sum(m, n));
        Main::put("sha512_256", SHA512::Sum256(m, n));
        Main::put("sha3_224", SHA3::Sum224(m, n));
        Main::put("sha3_256", SHA3::Sum256(m, n));
        Main::put("sha3_384", SHA3::Sum384(m, n));
        Main::put("sha3_512", SHA3::Sum512(m, n));
        Main::put("shake128", SHA3::Shake128(m, n, 40U));
        Main::put("shake256", SHA3::Shake256(m, n, 24U));
        Main::put("blake2b", BLAKE2B::Sum(m, n));
        Main::put("blake2s", BLAKE2S::Sum(m, n));
        var Digest* b2 = BLAKE2B::New(32U).0;
        b2.Write(m, n);
        Main::put("blake2b256", b2.Sum());
        Main::check("blake2bTooLong", BLAKE2B::New(65U).1 != null);

        var Digest* d = SHA256::New();
        d.Write(m, 1U);
        d.Write(m + 1U, 2U);
        var str first = Main::hex(d.Sum());
        var str again = Main::hex(d.Sum());
        d.Reset();
        d.WriteStr("abc");
        Main::check("stream", first == again && Main::hex(d.Sum()) == first && d.Size() == 32U && d.BlockSize() == 64U);
        var Hash* h = SHA3::New256();
        h.Write(m, n);
        Main::check("hash", Main::hex(h.Sum()) == Main::hex(SHA3::Sum256(m, n)) && h.Size() == 32U);

        var str keyStr = "key";
        var (byte*, int) key = Strs::ToBytes(keyStr);
        var str foxStr = "The quick brown fox jumps over the lazy dog";
        var (byte*, int) fox = Strs::ToBytes(foxStr);
        var Arr[byte]* tag = HMAC::Of(Digest.Kind::SHA256, key.0, 3U, fox.0, fox.1{uint}).0;
        Main::put("hmac256", tag);
        var HMAC* mac = HMAC::New(Digest.Kind::SHA512, key.0, 3U).0;
        mac.WriteStr(foxStr);
        var str macFirst = Main::hex(mac.Sum());
        File::StdOut.WriteStr("hmac512 " + macFirst + "\n");
        mac.Reset();
        mac.Write(fox.0, fox.1{uint});
        Main::check("hmacReset", Main::hex(mac.Sum()) == macFirst && mac.Size() == 64U && mac.BlockSize() == 128U);
        Main::check("hmacVerify",
            HMAC::Verify(Digest.Kind::SHA256, key.0, 3U, fox.0, fox.1{uint}, tag.Arr(), tag.Len()) &&
            !HMAC::Verify(Digest.Kind::SHA256, key.0, 3U, fox.0, fox.1{uint} - 1U, tag.Arr(), tag.Len()) &&
            !HMAC::Verify(Digest.Kind::SHA256, key.0, 3U, fox.0, fox.1{uint}, tag.Arr(), 31U));

        var Arr[byte]* polyKey = Main::unhex("85d6be7857556d337f4452fe42d506a80103808afb0db2fd4abff6af4149f51b");
        var str polyStr = "Cryptographic Forum Research Group";
        var (byte*, int) polyMsg = Strs::ToBytes(polyStr);
        var Arr[byte]* polyTag = Poly1305::Sum(polyKey.Arr(), 32U, polyMsg.0, polyMsg.1{uint}).0;
        Main::put("poly1305", polyTag);
        Main::check("poly1305Verify",
            Poly1305::Verify(polyKey.Arr(), 32U, polyMsg.0, polyMsg.1{uint}, polyTag.Arr(), 16U) &&
            !Poly1305::Verify(polyKey.Arr(), 32U, polyMsg.0, 3U, polyTag.Arr(), 16U) &&
            Poly1305::Sum(polyKey.Arr(), 16U, polyMsg.0, 3U).1 != null);

        var str secretStr = "secret";
        var (byte*, int) secret = Strs::ToBytes(secretStr);
        var str saltStr = "salt";
        var (byte*, int) salt = Strs::ToBytes(saltStr);
        var str infoStr = "info";
        var (byte*, int) info = Strs::ToBytes(infoStr);
        var Arr[byte]* okm = HKDF::Key(Digest.Kind::SHA256, secret.0, 6U, salt.0, 4U, info.0, 4U, 42U).0;
        Main::put("hkdf", okm);
        var Arr[byte]* prk = HKDF::Extract(Digest.Kind::SHA256, secret.0, 6U, salt.0, 4U).0;
        var Arr[byte]* expanded = HKDF::Expand(Digest.Kind::SHA256, prk.Arr(), prk.Len(), info.0, 4U, 42U).0;
        Main::check("hkdfParts", Main::hex(expanded) == Main::hex(okm));
        var str passwordStr = "password";
        var (byte*, int) password = Strs::ToBytes(passwordStr);
        Main::put("pbkdf2", PBKDF2::Key(Digest.Kind::SHA256, password.0, 8U, salt.0, 4U, 4096U, 32U).0);
        var str naclStr = "NaCl";
        var (byte*, int) nacl = Strs::ToBytes(naclStr);
        Main::put("scrypt", Scrypt::Key(password.0, 8U, nacl.0, 4U, 1024U, 8U, 16U, 64U).0);
        Main::check("scryptBad", Scrypt::Key(password.0, 8U, nacl.0, 4U, 1000U, 8U, 16U, 64U).1 != null);
        var str someSaltStr = "somesalt0";
        var (byte*, int) someSalt = Strs::ToBytes(someSaltStr);
        Main::put("argon2", Argon2::Key(password.0, 8U, someSalt.0, 9U, 2U, 1024U, 2U, 32U).0);

        var str hunterStr = "hunter2";
        var (byte*, int) hunter = Strs::ToBytes(hunterStr);
        var str wrongStr = "hunter3";
        var (byte*, int) wrong = Strs::ToBytes(wrongStr);
        var str argonHash = Argon2::Hash(hunter.0, 7U, 2U, 1024U).0;
        Main::check("argon2Hash", Strs::Split(argonHash, '$'{byte}).Get(1U) == "argon2id" &&
            Argon2::Verify(argonHash, hunter.0, 7U) && !Argon2::Verify(argonHash, wrong.0, 7U) &&
            !Argon2::Verify("nonsense", hunter.0, 7U));
        var str bcryptHash = BCrypt::Hash(hunter.0, 7U, 4).0;
        var str horseStr = "correct horse";
        var (byte*, int) horse = Strs::ToBytes(horseStr);
        var str longStr = "0123456789012345678901234567890123456789012345678901234567890123456789012";
        var (byte*, int) long = Strs::ToBytes(longStr);
        Main::check("bcrypt", BCrypt::Verify(bcryptHash, hunter.0, 7U) &&
            !BCrypt::Verify(bcryptHash, wrong.0, 7U) &&
            BCrypt::Verify("$2y$05$MvkiluqVjxioMVZkrMGUiOS/ewMzshqFuQ6Oua33CZXDRyz9W6FJO", horse.0, 13U) &&
            !BCrypt::Verify("$2y$05$MvkiluqVjxioMVZkrMGUiOS/ewMzshqFuQ6Oua33CZXDRyz9W6FJO", hunter.0, 7U) &&
            BCrypt::Hash(hunter.0, 7U, 3).1 != null &&
            BCrypt::Hash(long.0, 73U, 4).1 != null);

        var Arr[byte]* key16 = Main::unhex("000102030405060708090a0b0c0d0e0f");
        var Arr[byte]* iv = Main::unhex("101112131415161718191a1b1c1d1e1f");
        var Arr[byte]* nonce12 = Main::unhex("202122232425262728292a2b");
        var str aadStr = "header";
        var (byte*, int) aad = Strs::ToBytes(aadStr);
        var AEAD* gcm = AES::GCM(key16.Arr(), 16U).0;
        var Arr[byte]* sealed = gcm.Seal(nonce12.Arr(), fox.0, fox.1{uint}, aad.0, 6U);
        Main::put("gcm", sealed);
        var Arr[byte]* opened = gcm.Open(nonce12.Arr(), sealed.Arr(), sealed.Len(), aad.0, 6U).0;
        var bool aadWrong = gcm.Open(nonce12.Arr(), sealed.Arr(), sealed.Len(), aad.0, 5U).1 != null;
        sealed.Set(0U, (sealed.Get(0U){uint} + 1U){byte});
        Main::check("gcmOpen", Main::is(opened, foxStr) && aadWrong &&
            gcm.Open(nonce12.Arr(), sealed.Arr(), sealed.Len(), aad.0, 6U).1 != null &&
            gcm.Open(nonce12.Arr(), sealed.Arr(), 15U, aad.0, 6U).1 != null &&
            AES::GCM(key16.Arr(), 15U).1 != null && gcm.NonceSize() == 12U && gcm.Overhead() == 16U);
        var Arr[byte]* cbc = AES::EncryptCBC(key16.Arr(), 16U, iv.Arr(), fox.0, fox.1{uint}).0;
        Main::put("cbc", cbc);
        Main::check("cbcOpen", Main::is(AES::DecryptCBC(key16.Arr(), 16U, iv.Arr(), cbc.Arr(), cbc.Len()).0, foxStr) &&
            AES::DecryptCBC(key16.Arr(), 16U, iv.Arr(), cbc.Arr(), 20U).1 != null &&
            AES::EncryptCBC(key16.Arr(), 12U, iv.Arr(), fox.0, 3U).1 != null);
        var Arr[byte]* ctr = AES::CTR(key16.Arr(), 16U, iv.Arr(), fox.0, fox.1{uint}).0;
        Main::put("ctr", ctr);
        Main::check("ctrOpen", Main::is(AES::CTR(key16.Arr(), 16U, iv.Arr(), ctr.Arr(), ctr.Len()).0, foxStr));

        var Arr[byte]* chachaKey = Main::unhex("808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f");
        var Arr[byte]* chachaNonce = Main::unhex("070000004041424344454647");
        var Arr[byte]* chachaAad = Main::unhex("50515253c0c1c2c3c4c5c6c7");
        var str sunStr = "Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.";
        var (byte*, int) sun = Strs::ToBytes(sunStr);
        var AEAD* chacha = ChaCha20Poly1305::New(chachaKey.Arr(), 32U).0;
        var Arr[byte]* chachaSealed = chacha.Seal(chachaNonce.Arr(), sun.0, sun.1{uint}, chachaAad.Arr(), 12U);
        Main::put("chacha", chachaSealed);
        Main::check("chachaOpen", Main::is(chacha.Open(chachaNonce.Arr(), chachaSealed.Arr(), chachaSealed.Len(), chachaAad.Arr(), 12U).0, sunStr) &&
            chacha.Open(chachaNonce.Arr(), chachaSealed.Arr(), chachaSealed.Len(), chachaAad.Arr(), 11U).1 != null &&
            ChaCha20Poly1305::New(chachaKey.Arr(), 16U).1 != null);
        var AEAD* xchacha = ChaCha20Poly1305::NewX(chachaKey.Arr(), 32U).0;
        var Arr[byte]* nonce24 = Rand::Bytes(24U);
        var Arr[byte]* xSealed = xchacha.Seal(nonce24.Arr(), sun.0, sun.1{uint}, chachaAad.Arr(), 12U);
        Main::check("xchacha", xSealed.Len() == sun.1{uint} + 16U && xchacha.NonceSize() == 24U &&
            Main::is(xchacha.Open(nonce24.Arr(), xSealed.Arr(), xSealed.Len(), chachaAad.Arr(), 12U).0, sunStr) &&
            xchacha.Open(nonce24.Arr(), xSealed.Arr(), xSealed.Len(), null, 0U).1 != null);

        var AEAD* box = Salsa20::SecretBox(chachaKey.Arr(), 32U).0;
        var uint foxLen = fox.1{uint};
        var Arr[byte]* boxed = box.Seal(nonce24.Arr(), fox.0, foxLen, null, 0U);
        var byte* padded = new(byte, 32U + foxLen);
        Arrs::[byte]Copy(foxLen, fox.0, padded + 32U);
        var Arr[byte]* stream = Salsa20::XOR(chachaKey.Arr(), 32U, nonce24.Arr(), 24U, 0U, padded, 32U + foxLen).0;
        free(padded);
        Main::check("secretbox", boxed.Len() == foxLen + 16U &&
            Subtle::Equal(boxed.Arr() + 16U, stream.Arr() + 32U, foxLen) &&
            Poly1305::Verify(stream.Arr(), 32U, stream.Arr() + 32U, foxLen, boxed.Arr(), 16U) &&
            Main::is(box.Open(nonce24.Arr(), boxed.Arr(), boxed.Len(), null, 0U).0, foxStr) &&
            box.Open(nonce24.Arr(), boxed.Arr(), boxed.Len() - 1U, null, 0U).1 != null);
        var byte* zeros = new(byte, 128U);
        var Arr[byte]* whole = Salsa20::XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 8U, 0U, zeros, 128U).0;
        var Arr[byte]* second = Salsa20::XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 8U, 1U, zeros, 64U).0;
        free(zeros);
        var Arr[byte]* there = Salsa20::XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 8U, 0U, fox.0, foxLen).0;
        Main::check("salsa20", Subtle::Equal(whole.Arr() + 64U, second.Arr(), 64U) &&
            Main::is(Salsa20::XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 8U, 0U, there.Arr(), foxLen).0, foxStr) &&
            Salsa20::XOR(chachaKey.Arr(), 32U, nonce12.Arr(), 12U, 0U, fox.0, foxLen).1 != null);

        var bool below = true;
        for(var int i = 0; i < 100; i++) {
            below = below && Rand::UintN(10U) < 10U;
        }
        var Arr[byte]* r1 = Rand::Bytes(32U);
        var Arr[byte]* r2 = Rand::Bytes(32U);
        Main::check("rand", below && r1.Len() == 32U && Main::hex(r1) != Main::hex(r2));
        Main::check("subtle", Subtle::EqualStr("abc", "abc") && !Subtle::EqualStr("abc", "abd") &&
            !Subtle::EqualStr("ab", "abc") && Subtle::Equal(m, m, 0U));
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
