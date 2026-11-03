//go:build go1.27

package build_test

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/mldsa"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAsymmetric runs every asymmetric namespace, handing what zam makes to
// Go's own implementations to check — signatures, ciphertexts, shared secrets
// and encapsulations — and checking round trips, kind checks and tampering in
// zam itself.
func TestAsymmetric(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("codec/text/*");
import std("crypto/hash/*");
import std("crypto/asymmetric/*");

namespace Main {
    static fun str(Arr[byte]*) hex = (a) {
        return type(Hex).Encode(a.Arr(), a.Len());
    }

    static fun Arr[byte]*(str) unhex = (s) {
        return type(Hex).Decode(s).first;
    }

    static fun void(str, str) put = (name, value) {
        type(File).StdOut.WriteStr(name + " " + value + "\n");
    }

    static fun void(str, bool) check = (name, ok) {
        type(Main).put(name, ok{str});
    }

    fun void(uint, str...) main = (argC, argV) {
        var str msgStr = "attack at dawn";
        var Pair[byte*, int] msg = type(Strs).ToBytes(msgStr);
        var byte* m = msg.first;
        var uint n = msg.second{uint};

        var Key* ed = type(Ed25519).Generate();
        var Arr[byte]* edSig = type(Ed25519).Sign(ed, m, n).first;
        type(Main).put("ed25519Pub", type(Main).hex(ed.Public().DER().first));
        type(Main).put("ed25519Sig", type(Main).hex(edSig));
        var Arr[byte]* seed = ed.Raw().first;
        var Key* again = type(Ed25519).PrivateKey(seed.Arr(), seed.Len()).first;
        type(Main).check("ed25519", type(Ed25519).Verify(ed.Public(), m, n, edSig.Arr(), edSig.Len()) &&
            !type(Ed25519).Verify(ed, m, n - 1U, edSig.Arr(), edSig.Len()) &&
            type(Main).hex(again.Public().Raw().first) == type(Main).hex(ed.Public().Raw().first) &&
            type(Ed25519).Sign(ed.Public(), m, n).second != null &&
            type(Ed25519).Sign(type(X25519).Generate(), m, n).second != null &&
            seed.Len() == 32U);

        var Key* ec = type(ECDSA).Generate(type(Key.Kind).P256).first;
        var Arr[byte]* ecSig = type(ECDSA).Sign(ec, type(Digest.Kind).SHA256, m, n).first;
        type(Main).put("ecdsaPub", type(Main).hex(ec.Public().DER().first));
        type(Main).put("ecdsaSig", type(Main).hex(ecSig));
        var Key* ecRead = type(Key).ParsePEM(ec.PEM().first).first;
        var Arr[byte]* ecPubDer = ec.Public().DER().first;
        var Key* ecPub = type(Key).ParseDER(ecPubDer.Arr(), ecPubDer.Len()).first;
        type(Main).check("ecdsa", type(ECDSA).Verify(ecPub, type(Digest.Kind).SHA256, m, n, ecSig.Arr(), ecSig.Len()) &&
            !type(ECDSA).Verify(ecPub, type(Digest.Kind).SHA384, m, n, ecSig.Arr(), ecSig.Len()) &&
            ecRead.IsPrivate() && ecRead.Kind() == type(Key.Kind).P256 && !ecPub.IsPrivate() &&
            type(ECDSA).Generate(type(Key.Kind).RSA).second != null &&
            type(Key).ParsePEM("nonsense").second != null);

        var Key* p384 = type(ECDH).Generate(type(Key.Kind).P384).first;
        var Key* q384 = type(ECDH).Generate(type(Key.Kind).P384).first;
        var Arr[byte]* pq = type(ECDH).Derive(p384, q384.Public()).first;
        var Arr[byte]* qp = type(ECDH).Derive(q384, p384.Public()).first;
        type(Main).check("ecdh", pq.Len() == 48U && type(Main).hex(pq) == type(Main).hex(qp) &&
            type(ECDH).Derive(p384, ec.Public()).second != null);

        var Key* rsa = type(RSA).Generate(2048U).first;
        var Arr[byte]* pss = type(RSA).Sign(rsa, type(Digest.Kind).SHA256, type(RSA.Padding).PSS, m, n).first;
        var Arr[byte]* v15 = type(RSA).Sign(rsa, type(Digest.Kind).SHA256, type(RSA.Padding).PKCS1V15, m, n).first;
        var Arr[byte]* sealed = type(RSA).Encrypt(rsa.Public(), type(Digest.Kind).SHA256, m, n).first;
        type(Main).put("rsaKey", type(Main).hex(rsa.DER().first));
        type(Main).put("rsaPSS", type(Main).hex(pss));
        type(Main).put("rsaV15", type(Main).hex(v15));
        type(Main).put("rsaOAEP", type(Main).hex(sealed));
        var Arr[byte]* opened = type(RSA).Decrypt(rsa, type(Digest.Kind).SHA256, sealed.Arr(), sealed.Len()).first;
        type(Main).check("rsa", type(Strs).FromBytes(opened.Arr(), opened.Len(){int}) == msgStr &&
            type(RSA).Verify(rsa.Public(), type(Digest.Kind).SHA256, type(RSA.Padding).PSS, m, n, pss.Arr(), pss.Len()) &&
            !type(RSA).Verify(rsa.Public(), type(Digest.Kind).SHA256, type(RSA.Padding).PKCS1V15, m, n, pss.Arr(), pss.Len()) &&
            type(RSA).Decrypt(rsa.Public(), type(Digest.Kind).SHA256, sealed.Arr(), sealed.Len()).second != null &&
            type(RSA).Decrypt(rsa, type(Digest.Kind).SHA1, sealed.Arr(), sealed.Len()).second != null &&
            type(RSA).Generate(1024U).second != null && rsa.Kind() == type(Key.Kind).RSA);

        var Arr[byte]* goX = type(Main).unhex(argV[0]);
        var Key* peer = type(X25519).PublicKey(goX.Arr(), goX.Len()).first;
        var Key* x = type(X25519).Generate();
        type(Main).put("x25519Pub", type(Main).hex(x.Public().Raw().first));
        type(Main).put("x25519Secret", type(Main).hex(type(X25519).Derive(x, peer).first));
        type(Main).check("x25519", type(X25519).Derive(peer, x).second != null && type(X25519).PublicKey(goX.Arr(), 31U).second != null);

        var Arr[byte]* ek = type(Main).unhex(argV[1]);
        var Key* goKem = type(Key).FromRaw(type(Key.Kind).ML_KEM_768, ek.Arr(), ek.Len(), false).first;
        var MLKEM.Encapsulation* toGo = type(MLKEM).Encapsulate(goKem).first;
        type(Main).put("mlkemCiphertext", type(Main).hex(toGo.ciphertext));
        type(Main).put("mlkemSecret", type(Main).hex(toGo.secret));
        var Key* kem = type(MLKEM).Generate(type(Key.Kind).ML_KEM_1024).first;
        var MLKEM.Encapsulation* enc = type(MLKEM).Encapsulate(kem.Public()).first;
        var Arr[byte]* secret = type(MLKEM).Decapsulate(kem, enc.ciphertext.Arr(), enc.ciphertext.Len()).first;
        type(Main).check("mlkem", type(Main).hex(secret) == type(Main).hex(enc.secret) && secret.Len() == 32U &&
            type(MLKEM).Decapsulate(goKem, enc.ciphertext.Arr(), enc.ciphertext.Len()).second != null &&
            type(MLKEM).Generate(type(Key.Kind).ML_DSA_44).second != null);

        var Key* dsa = type(MLDSA).Generate(type(Key.Kind).ML_DSA_65).first;
        var Arr[byte]* dsaSig = type(MLDSA).Sign(dsa, m, n).first;
        type(Main).put("mldsaPub", type(Main).hex(dsa.Public().Raw().first));
        type(Main).put("mldsaSig", type(Main).hex(dsaSig));
        var Key* dsaRead = type(Key).ParsePEM(dsa.Public().PEM().first).first;
        type(Main).check("mldsa", type(MLDSA).Verify(dsaRead, m, n, dsaSig.Arr(), dsaSig.Len()) &&
            !type(MLDSA).Verify(dsaRead, m, n - 1U, dsaSig.Arr(), dsaSig.Len()) &&
            dsaRead.Kind() == type(Key.Kind).ML_DSA_65);

        var str infoStr = "zam hpke";
        var Pair[byte*, int] info = type(Strs).ToBytes(infoStr);
        var str aadStr = "header";
        var Pair[byte*, int] aad = type(Strs).ToBytes(aadStr);
        var Arr[byte]* goHpke = type(Main).unhex(argV[2]);
        var Key* recipient = type(X25519).PublicKey(goHpke.Arr(), goHpke.Len()).first;
        var HPKE.Suite suite = type(HPKE).Of(type(HPKE.KEM).X25519_SHA256, type(HPKE.KDF).SHA256, type(HPKE.Cipher).AES_128_GCM);
        var HPKE.Sealed* toGoHpke = type(HPKE).Seal(suite, recipient, info.first, 8U, aad.first, 6U, m, n).first;
        type(Main).put("hpkeEnc", type(Main).hex(toGoHpke.enc));
        type(Main).put("hpkeCiphertext", type(Main).hex(toGoHpke.ciphertext));
        var HPKE.Suite p256 = type(HPKE).Of(type(HPKE.KEM).P256_SHA256, type(HPKE.KDF).SHA384, type(HPKE.Cipher).CHACHA20_POLY1305);
        var HPKE.Sealed* local = type(HPKE).Seal(p256, ec.Public(), info.first, 8U, aad.first, 6U, m, n).first;
        var Arr[byte]* hpkeOpened = type(HPKE).Open(p256, ec, local, info.first, 8U, aad.first, 6U).first;
        type(Main).check("hpke", type(Strs).FromBytes(hpkeOpened.Arr(), hpkeOpened.Len(){int}) == msgStr &&
            type(HPKE).Open(p256, ec, local, info.first, 8U, aad.first, 5U).second != null &&
            type(HPKE).Open(p256, ec.Public(), local, info.first, 8U, aad.first, 6U).second != null &&
            type(HPKE).Seal(suite, ec.Public(), info.first, 8U, aad.first, 6U, m, n).second != null);
    }
}
`)
	goX, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	goKem, err := mlkem.GenerateKey768()
	require.NoError(t, err)
	goHpke, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)

	stdout, stderr, status := run(t, binary,
		hex.EncodeToString(goX.PublicKey().Bytes()),
		hex.EncodeToString(goKem.EncapsulationKey().Bytes()),
		hex.EncodeToString(goHpke.PublicKey().Bytes()))
	require.Zero(t, status, stderr)

	got := map[string]string{}
	for line := range strings.Lines(stdout) {
		name, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		got[name] = value
	}
	for _, name := range []string{"ed25519", "ecdsa", "ecdh", "rsa", "x25519", "mlkem", "mldsa", "hpke"} {
		assert.Equal(t, "true", got[name], name)
	}
	bytesOf := func(name string) []byte {
		b, err := hex.DecodeString(got[name])
		require.NoError(t, err, name)
		return b
	}
	msg := []byte("attack at dawn")
	digest := sha256.Sum256(msg)

	edPub, err := x509.ParsePKIXPublicKey(bytesOf("ed25519Pub"))
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(edPub.(ed25519.PublicKey), msg, bytesOf("ed25519Sig")), "ed25519 signature")

	ecPub, err := x509.ParsePKIXPublicKey(bytesOf("ecdsaPub"))
	require.NoError(t, err)
	assert.True(t, ecdsa.VerifyASN1(ecPub.(*ecdsa.PublicKey), digest[:], bytesOf("ecdsaSig")), "ecdsa signature")

	rsaKey, err := x509.ParsePKCS8PrivateKey(bytesOf("rsaKey"))
	require.NoError(t, err)
	priv := rsaKey.(*rsa.PrivateKey)
	assert.NoError(t, rsa.VerifyPSS(&priv.PublicKey, crypto.SHA256, digest[:], bytesOf("rsaPSS"), nil), "rsa pss")
	assert.NoError(t, rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, digest[:], bytesOf("rsaV15")), "rsa pkcs1v15")
	plain, err := rsa.DecryptOAEP(sha256.New(), nil, priv, bytesOf("rsaOAEP"), nil)
	require.NoError(t, err)
	assert.Equal(t, msg, plain)

	zamX, err := ecdh.X25519().NewPublicKey(bytesOf("x25519Pub"))
	require.NoError(t, err)
	shared, err := goX.ECDH(zamX)
	require.NoError(t, err)
	assert.Equal(t, shared, bytesOf("x25519Secret"))

	kemSecret, err := goKem.Decapsulate(bytesOf("mlkemCiphertext"))
	require.NoError(t, err)
	assert.Equal(t, kemSecret, bytesOf("mlkemSecret"))

	dsaPub, err := mldsa.NewPublicKey(mldsa.MLDSA65(), bytesOf("mldsaPub"))
	require.NoError(t, err)
	assert.NoError(t, mldsa.Verify(dsaPub, msg, bytesOf("mldsaSig"), nil), "ml-dsa signature")

	hpkeKey, err := hpke.NewDHKEMPrivateKey(goHpke)
	require.NoError(t, err)
	recipient, err := hpke.NewRecipient(bytesOf("hpkeEnc"), hpkeKey, hpke.HKDFSHA256(), hpke.AES128GCM(), []byte("zam hpke"))
	require.NoError(t, err)
	opened, err := recipient.Open([]byte("header"), bytesOf("hpkeCiphertext"))
	require.NoError(t, err)
	assert.Equal(t, msg, opened)

}
