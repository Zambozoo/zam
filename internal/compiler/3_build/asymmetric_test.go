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
        return Hex::Encode(a.Arr(), a.Len());
    }

    static fun Arr[byte]*(str) unhex = (s) {
        return Hex::Decode(s).0;
    }

    static fun void(str, str) put = (name, value) {
        File::StdOut.WriteStr(name + " " + value + "\n");
    }

    static fun void(str, bool) check = (name, ok) {
        Main::put(name, ok{str});
    }

    fun void(uint, str...) main = (argC, argV) {
        var str msgStr = "attack at dawn";
        var (byte*, int) msg = Strs::ToBytes(msgStr);
        var byte* m = msg.0;
        var uint n = msg.1{uint};

        var Key* ed = Ed25519::Generate();
        var Arr[byte]* edSig = Ed25519::Sign(ed, m, n).0;
        Main::put("ed25519Pub", Main::hex(ed.Public().DER().0));
        Main::put("ed25519Sig", Main::hex(edSig));
        var Arr[byte]* seed = ed.Raw().0;
        var Key* again = Ed25519::PrivateKey(seed.Arr(), seed.Len()).0;
        Main::check("ed25519", Ed25519::Verify(ed.Public(), m, n, edSig.Arr(), edSig.Len()) &&
            !Ed25519::Verify(ed, m, n - 1U, edSig.Arr(), edSig.Len()) &&
            Main::hex(again.Public().Raw().0) == Main::hex(ed.Public().Raw().0) &&
            Ed25519::Sign(ed.Public(), m, n).1 != null &&
            Ed25519::Sign(X25519::Generate(), m, n).1 != null &&
            seed.Len() == 32U);

        var Key* ec = ECDSA::Generate(Key.Kind::P256).0;
        var Arr[byte]* ecSig = ECDSA::Sign(ec, Digest.Kind::SHA256, m, n).0;
        Main::put("ecdsaPub", Main::hex(ec.Public().DER().0));
        Main::put("ecdsaSig", Main::hex(ecSig));
        var Key* ecRead = Key::ParsePEM(ec.PEM().0).0;
        var Arr[byte]* ecPubDer = ec.Public().DER().0;
        var Key* ecPub = Key::ParseDER(ecPubDer.Arr(), ecPubDer.Len()).0;
        Main::check("ecdsa", ECDSA::Verify(ecPub, Digest.Kind::SHA256, m, n, ecSig.Arr(), ecSig.Len()) &&
            !ECDSA::Verify(ecPub, Digest.Kind::SHA384, m, n, ecSig.Arr(), ecSig.Len()) &&
            ecRead.IsPrivate() && ecRead.Kind() == Key.Kind::P256 && !ecPub.IsPrivate() &&
            ECDSA::Generate(Key.Kind::RSA).1 != null &&
            Key::ParsePEM("nonsense").1 != null);

        var Key* p384 = ECDH::Generate(Key.Kind::P384).0;
        var Key* q384 = ECDH::Generate(Key.Kind::P384).0;
        var Arr[byte]* pq = ECDH::Derive(p384, q384.Public()).0;
        var Arr[byte]* qp = ECDH::Derive(q384, p384.Public()).0;
        Main::check("ecdh", pq.Len() == 48U && Main::hex(pq) == Main::hex(qp) &&
            ECDH::Derive(p384, ec.Public()).1 != null);

        var Key* rsa = RSA::Generate(2048U).0;
        var Arr[byte]* pss = RSA::Sign(rsa, Digest.Kind::SHA256, RSA.Padding::PSS, m, n).0;
        var Arr[byte]* v15 = RSA::Sign(rsa, Digest.Kind::SHA256, RSA.Padding::PKCS1V15, m, n).0;
        var Arr[byte]* sealed = RSA::Encrypt(rsa.Public(), Digest.Kind::SHA256, m, n).0;
        Main::put("rsaKey", Main::hex(rsa.DER().0));
        Main::put("rsaPSS", Main::hex(pss));
        Main::put("rsaV15", Main::hex(v15));
        Main::put("rsaOAEP", Main::hex(sealed));
        var Arr[byte]* opened = RSA::Decrypt(rsa, Digest.Kind::SHA256, sealed.Arr(), sealed.Len()).0;
        Main::check("rsa", Strs::FromBytes(opened.Arr(), opened.Len(){int}) == msgStr &&
            RSA::Verify(rsa.Public(), Digest.Kind::SHA256, RSA.Padding::PSS, m, n, pss.Arr(), pss.Len()) &&
            !RSA::Verify(rsa.Public(), Digest.Kind::SHA256, RSA.Padding::PKCS1V15, m, n, pss.Arr(), pss.Len()) &&
            RSA::Decrypt(rsa.Public(), Digest.Kind::SHA256, sealed.Arr(), sealed.Len()).1 != null &&
            RSA::Decrypt(rsa, Digest.Kind::SHA1, sealed.Arr(), sealed.Len()).1 != null &&
            RSA::Generate(1024U).1 != null && rsa.Kind() == Key.Kind::RSA);

        var Arr[byte]* goX = Main::unhex(argV[0]);
        var Key* peer = X25519::PublicKey(goX.Arr(), goX.Len()).0;
        var Key* x = X25519::Generate();
        Main::put("x25519Pub", Main::hex(x.Public().Raw().0));
        Main::put("x25519Secret", Main::hex(X25519::Derive(x, peer).0));
        Main::check("x25519", X25519::Derive(peer, x).1 != null && X25519::PublicKey(goX.Arr(), 31U).1 != null);

        var Arr[byte]* ek = Main::unhex(argV[1]);
        var Key* goKem = Key::FromRaw(Key.Kind::ML_KEM_768, ek.Arr(), ek.Len(), false).0;
        var MLKEM.Encapsulation* toGo = MLKEM::Encapsulate(goKem).0;
        Main::put("mlkemCiphertext", Main::hex(toGo.ciphertext));
        Main::put("mlkemSecret", Main::hex(toGo.secret));
        var Key* kem = MLKEM::Generate(Key.Kind::ML_KEM_1024).0;
        var MLKEM.Encapsulation* enc = MLKEM::Encapsulate(kem.Public()).0;
        var Arr[byte]* secret = MLKEM::Decapsulate(kem, enc.ciphertext.Arr(), enc.ciphertext.Len()).0;
        Main::check("mlkem", Main::hex(secret) == Main::hex(enc.secret) && secret.Len() == 32U &&
            MLKEM::Decapsulate(goKem, enc.ciphertext.Arr(), enc.ciphertext.Len()).1 != null &&
            MLKEM::Generate(Key.Kind::ML_DSA_44).1 != null);

        var Key* dsa = MLDSA::Generate(Key.Kind::ML_DSA_65).0;
        var Arr[byte]* dsaSig = MLDSA::Sign(dsa, m, n).0;
        Main::put("mldsaPub", Main::hex(dsa.Public().Raw().0));
        Main::put("mldsaSig", Main::hex(dsaSig));
        var Key* dsaRead = Key::ParsePEM(dsa.Public().PEM().0).0;
        Main::check("mldsa", MLDSA::Verify(dsaRead, m, n, dsaSig.Arr(), dsaSig.Len()) &&
            !MLDSA::Verify(dsaRead, m, n - 1U, dsaSig.Arr(), dsaSig.Len()) &&
            dsaRead.Kind() == Key.Kind::ML_DSA_65);

        var str infoStr = "zam hpke";
        var (byte*, int) info = Strs::ToBytes(infoStr);
        var str aadStr = "header";
        var (byte*, int) aad = Strs::ToBytes(aadStr);
        var Arr[byte]* goHpke = Main::unhex(argV[2]);
        var Key* recipient = X25519::PublicKey(goHpke.Arr(), goHpke.Len()).0;
        var HPKE.Suite suite = HPKE::Of(HPKE.KEM::X25519_SHA256, HPKE.KDF::SHA256, HPKE.Cipher::AES_128_GCM);
        var HPKE.Sealed* toGoHpke = HPKE::Seal(suite, recipient, info.0, 8U, aad.0, 6U, m, n).0;
        Main::put("hpkeEnc", Main::hex(toGoHpke.enc));
        Main::put("hpkeCiphertext", Main::hex(toGoHpke.ciphertext));
        var HPKE.Suite p256 = HPKE::Of(HPKE.KEM::P256_SHA256, HPKE.KDF::SHA384, HPKE.Cipher::CHACHA20_POLY1305);
        var HPKE.Sealed* local = HPKE::Seal(p256, ec.Public(), info.0, 8U, aad.0, 6U, m, n).0;
        var Arr[byte]* hpkeOpened = HPKE::Open(p256, ec, local, info.0, 8U, aad.0, 6U).0;
        Main::check("hpke", Strs::FromBytes(hpkeOpened.Arr(), hpkeOpened.Len(){int}) == msgStr &&
            HPKE::Open(p256, ec, local, info.0, 8U, aad.0, 5U).1 != null &&
            HPKE::Open(p256, ec.Public(), local, info.0, 8U, aad.0, 6U).1 != null &&
            HPKE::Seal(suite, ec.Public(), info.0, 8U, aad.0, 6U, m, n).1 != null);
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
