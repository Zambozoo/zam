package build_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPKI issues, reads and verifies certificates in both directions with
// Go's x509, bundles them with PKCS #12, asks an OCSP endpoint about them,
// and orders one from an ACME server that checks every request it is sent.
func TestPKI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	goCA, goCAKey := goCertificate(t, nil, nil, "Go Root", nil, true)
	goLeaf, _ := goCertificate(t, goCA, goCAKey, "go leaf", []string{"go.example.test"}, false)
	writePEM(t, filepath.Join(dir, "go-ca.pem"), goCA.Raw)
	writePEM(t, filepath.Join(dir, "go-leaf.pem"), goLeaf.Raw)

	var ocspMu sync.Mutex
	var ocspRequests [][]byte
	ocsp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ocspMu.Lock()
		ocspRequests = append(ocspRequests, body)
		ocspMu.Unlock()
		resp, err := os.ReadFile(filepath.Join(dir, "ocsp.der"))
		if err != nil || r.Header.Get("Content-Type") != "application/ocsp-request" {
			http.Error(w, "no response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(resp)
	}))
	defer ocsp.Close()

	acme := newFakeACME(t, goCA, goCAKey)
	server := httptest.NewServer(acme)
	defer server.Close()
	acme.base = server.URL

	binary := compile(t, `import std("lang/*");
import std("lang/time/*");
import std("io/local/*");
import std("codec/text/*");
import std("crypto/hash/*");
import std("crypto/asymmetric/*");
import std("crypto/pki/*");

namespace Main {
    static fun void(str, str) put = (name, value) {
        File::StdOut.WriteStr(name + " " + value + "\n");
    }

    static fun void(str, Err*) must = (what, e) {
        if(e != null) {
            Main::put("error", what + ": " + e.Err());
        }
    }

    static fun void(str, str) write = (path, s) {
        var File* f = File::Create(path).0;
        f.WriteStr(s);
        f.Close();
    }

    static fun str(str) read = (path) {
        var File* f = File::Open(path).0;
        var str s = f.ReadAll().0;
        f.Close();
        return s;
    }

    static fun Arr[X509*]*(X509*) one = (c) {
        var Arr[X509*]* a = Arr::[X509*]New(1U, new(X509*, 1U)...);
        a.Set(0U, c);
        return a;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str dir = argV[0];

        var X509* goCA = X509::ParsePEM(Main::read(dir + "/go-ca.pem")).0;
        var X509* goLeaf = X509::ParsePEM(Main::read(dir + "/go-leaf.pem")).0;
        Main::put("goLeaf", goLeaf.Subject() + "|" + goLeaf.Issuer() + "|" + goLeaf.DNSNames().Get(0U) + "|" + goLeaf.IsCA(){str} + "|" + goCA.IsCA(){str});
        Main::put("goVerify", (goLeaf.Verify(Main::one(goCA), null, "go.example.test", Time::Now()) == null){str} + " " +
            (goLeaf.Verify(Main::one(goCA), null, "other.test", Time::Now()) != null){str} + " " +
            (goLeaf.Verify(Main::one(goLeaf), null, "", Time::Now()) != null){str} + " " +
            (goLeaf.Verify(Main::one(goCA), null, "", goLeaf.NotAfter().Add(Duration::Seconds(10))) != null){str});

        var Key* caKey = ECDSA::Generate(Key.Kind::P384).0;
        var X509.Template* caT = X509.Template::New("Zam Root");
        caT.organization = "Zam";
        caT.isCA = true;
        var (X509*, Err*) ca = X509::SelfSign(caT, caKey);
        Main::must("self-sign", ca.1);
        var Key* leafKey = Ed25519::Generate();
        var X509.Template* t = X509.Template::New("zam leaf");
        t.dnsNames = Strs::Split("zam.example.test,www.zam.example.test", ','{byte});
        t.ocspURL = argV[1];
        var (X509*, Err*) leaf = X509::Sign(t, leafKey.Public(), ca.0, caKey);
        Main::must("sign", leaf.1);
        Main::write(dir + "/zam-ca.pem", ca.0.PEM());
        Main::write(dir + "/zam-leaf.pem", leaf.0.PEM());
        Main::put("zamLeafFingerprint", Hex::Encode(leaf.0.Fingerprint(Digest.Kind::SHA256).Arr(), 32U));
        var Arr[byte]* der = leaf.0.DER();
        var X509* back = X509::ParseDER(der.Arr(), der.Len()).0;
        Main::put("zamLeaf", back.Serial() + "|" + (back.Serial() == leaf.0.Serial()){str} + "|" + back.OCSPURL() + "|" + Hex::Encode(back.PublicKey().0.Raw().0.Arr(), 32U) + "|" + Hex::Encode(leafKey.Public().Raw().0.Arr(), 32U));
        Main::put("chain", X509::ParsePEMChain(ca.0.PEM() + leaf.0.PEM()).0.Len(){str} + " " + (X509::ParsePEM("junk").1 != null){str} +
            " " + (X509::SelfSign(caT, caKey.Public()).1 != null){str});

        var (Arr[byte]*, Err*) p12 = PKCS12::Create(leafKey, leaf.0, Main::one(ca.0), "secret", "zam leaf");
        Main::must("pkcs12", p12.1);
        var PKCS12.Bundle* bundle = PKCS12::Parse(p12.0.Arr(), p12.0.Len(), "secret").0;
        Main::put("pkcs12", (bundle.cert.Serial() == leaf.0.Serial()){str} + " " + (bundle.chain.Get(0U).Serial() == ca.0.Serial()){str} + " " +
            (Hex::Encode(bundle.key.Raw().0.Arr(), 32U) == Hex::Encode(leafKey.Raw().0.Arr(), 32U)){str} + " " +
            (PKCS12::Parse(p12.0.Arr(), p12.0.Len(), "wrong").1 != null){str});

        var Arr[byte]* req = OCSP::Request(leaf.0, ca.0).0;
        var (Arr[byte]*, Err*) resp = OCSP::Respond(req.Arr(), req.Len(), ca.0, caKey, OCSP.Status::Revoked, Time::FromUnix(1700000000, 0), Duration::Hours(1));
        Main::must("respond", resp.1);
        var File* f = File::Create(dir + "/ocsp.der").0;
        f.Write(resp.0.Arr(), resp.0.Len(){int});
        f.Close();
        Main::put("ocspRequest", Hex::Encode(req.Arr(), req.Len()));
        var (OCSP.Result*, Err*) queried = OCSP::Query(leaf.0, ca.0);
        Main::must("query", queried.1);
        Main::put("ocsp", (queried.0.status == OCSP.Status::Revoked){str} + " " + queried.0.revokedAt.Str() + " " +
            queried.0.nextUpdate.Sub(queried.0.thisUpdate).Str() + " " + queried.0.hasNextUpdate{str} + " " +
            (OCSP::Check(leaf.0, goCA, resp.0.Arr(), resp.0.Len()).1 != null){str} + " " +
            (OCSP::Query(goLeaf, goCA).1 != null){str});

        var Key* account = ECDSA::Generate(Key.Kind::P256).0;
        var (ACME*, Err*) client = ACME::New(argV[2], account);
        Main::must("acme", client.1);
        var ACME* acme = client.0;
        acme.pollMs = 10U;
        Main::must("register", acme.Register("admin@zam.example.test", true));
        var Arr[str]* domains = Strs::Split("a.zam.example.test,b.zam.example.test", ','{byte});
        var (ACME.Order*, Err*) order = acme.NewOrder(domains);
        Main::must("order", order.1);
        var Arr[ACME.Challenge*]* http = acme.Challenges(order.0, "http-01").0;
        var Arr[ACME.Challenge*]* dns = acme.Challenges(order.0, "dns-01").0;
        for(var uint i = 0U; i < http.Len(); i++) {
            Main::put("challenge", http.Get(i).domain + " " + http.Get(i).token + " " + http.Get(i).keyAuthorization + " " + dns.Get(i).dnsValue);
            Main::must("accept", acme.Accept(http.Get(i)));
        }
        Main::must("wait", acme.Wait(order.0));
        var Key* certKey = ECDSA::Generate(Key.Kind::P256).0;
        var Arr[byte]* csr = X509::Request(certKey, "a.zam.example.test", domains).0;
        Main::must("finalize", acme.Finalize(order.0, csr));
        var (str, Err*) chainPem = acme.Certificate(order.0);
        Main::must("certificate", chainPem.1);
        var Arr[X509*]* issued = X509::ParsePEMChain(chainPem.0).0;
        var Arr[byte]* issuedKey = issued.Get(0U).PublicKey().0.DER().0;
        var Arr[byte]* wantKey = certKey.Public().DER().0;
        Main::put("acme", order.0.status + " " + issued.Len(){str} + " " +
            (issued.Get(0U).Verify(Main::one(goCA), null, "b.zam.example.test", Time::Now()) == null){str} + " " +
            (Hex::Encode(issuedKey.Arr(), issuedKey.Len()) == Hex::Encode(wantKey.Arr(), wantKey.Len())){str} + " " +
            (ACME::New(argV[2], certKey.Public()).1 != null){str});
    }
}
`)
	stdout, stderr, status := run(t, binary, dir, ocsp.URL+"/ocsp", server.URL+"/dir")
	require.Zero(t, status, stderr)

	got := map[string][]string{}
	for line := range strings.Lines(stdout) {
		name, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		got[name] = append(got[name], value)
	}
	require.Empty(t, got["error"])
	first := func(name string) string {
		require.NotEmpty(t, got[name], name)
		return got[name][0]
	}

	assert.Equal(t, "CN=go leaf|CN=Go Root|go.example.test|false|true", first("goLeaf"))
	assert.Equal(t, "true true true true", first("goVerify"))

	zamCA := readPEM(t, filepath.Join(dir, "zam-ca.pem"))
	zamLeaf := readPEM(t, filepath.Join(dir, "zam-leaf.pem"))
	roots := x509.NewCertPool()
	roots.AddCert(zamCA)
	_, err := zamLeaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "www.zam.example.test"})
	assert.NoError(t, err)
	assert.Equal(t, "CN=Zam Root,O=Zam", zamCA.Subject.String())
	assert.True(t, zamCA.IsCA)
	assert.Equal(t, []string{"zam.example.test", "www.zam.example.test"}, zamLeaf.DNSNames)
	assert.Equal(t, []string{ocsp.URL + "/ocsp"}, zamLeaf.OCSPServer)
	fingerprint := sha256.Sum256(zamLeaf.Raw)
	assert.Equal(t, fmt.Sprintf("%x", fingerprint), first("zamLeafFingerprint"))
	parts := strings.Split(first("zamLeaf"), "|")
	require.Len(t, parts, 5)
	assert.Equal(t, strings.ToUpper(zamLeaf.SerialNumber.Text(16)), strings.TrimLeft(parts[0], "0"))
	assert.Equal(t, []string{"true", ocsp.URL + "/ocsp"}, parts[1:3])
	assert.Equal(t, parts[4], parts[3])
	assert.Equal(t, "2 true true", first("chain"))
	assert.Equal(t, "true true true true", first("pkcs12"))

	assert.Equal(t, "true 2023-11-14T22:13:20Z 1h0m0s true true true", first("ocsp"))
	ocspMu.Lock()
	require.Len(t, ocspRequests, 1)
	assert.Equal(t, first("ocspRequest"), fmt.Sprintf("%x", ocspRequests[0]))
	ocspMu.Unlock()

	acme.mu.Lock()
	defer acme.mu.Unlock()
	thumb := sha256.Sum256([]byte(acme.jwk))
	thumbprint := base64.RawURLEncoding.EncodeToString(thumb[:])
	require.Len(t, got["challenge"], 2)
	for i, line := range got["challenge"] {
		fields := strings.Fields(line)
		require.Len(t, fields, 4)
		token := fmt.Sprintf("token-%d", i)
		dnsSum := sha256.Sum256([]byte(token + "." + thumbprint))
		assert.Equal(t, []string{acme.domains[i], token, token + "." + thumbprint, base64.RawURLEncoding.EncodeToString(dnsSum[:])}, fields)
	}
	assert.Equal(t, "valid 2 true true true", first("acme"))
	assert.Equal(t, 1, acme.badNonces)
	assert.Equal(t, []string{"mailto:admin@zam.example.test"}, acme.contact)
	assert.Equal(t, []string{"a.zam.example.test", "b.zam.example.test"}, acme.csrNames)
}

// goCertificate issues a P-256 certificate with Go's x509, self-signed when
// parent is nil.
func goCertificate(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, name string, dns []string, ca bool) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		DNSNames:              dns,
		IsCA:                  ca,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	signer := key
	if parent == nil {
		parent = template
	} else {
		signer = parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, key
}

func writePEM(t *testing.T, path string, der []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
}

func readPEM(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	block, _ := pem.Decode(b)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

// fakeACME is just enough of an RFC 8555 server to issue one certificate,
// checking every JWS it is sent: its signature, its nonce and its URL. The
// first signed request is refused with badNonce, to be retried.
type fakeACME struct {
	t      *testing.T
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	base   string
	mu     sync.Mutex
	nonces map[string]bool
	nonce  int

	jwk        string
	key        *ecdsa.PublicKey
	badNonces  int
	contact    []string
	domains    []string
	accepted   map[int]bool
	polls      int
	issued     []byte
	csrNames   []string
	orderState string
}

func newFakeACME(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey) *fakeACME {
	return &fakeACME{t: t, ca: ca, caKey: caKey, nonces: map[string]bool{}, accepted: map[int]bool{}, orderState: "pending"}
}

func (f *fakeACME) freshNonce(w http.ResponseWriter) {
	f.nonce++
	n := fmt.Sprintf("nonce-%d", f.nonce)
	f.nonces[n] = true
	w.Header().Set("Replay-Nonce", n)
}

func (f *fakeACME) problem(w http.ResponseWriter, status int, kind, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"type": "urn:ietf:params:acme:error:" + kind, "detail": detail})
}

func (f *fakeACME) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeACME) order() map[string]any {
	auths := make([]string, len(f.domains))
	for i := range f.domains {
		auths[i] = fmt.Sprintf("%s/authz/%d", f.base, i)
	}
	o := map[string]any{"status": f.orderState, "authorizations": auths, "finalize": f.base + "/finalize"}
	if f.orderState == "valid" {
		o["certificate"] = f.base + "/cert"
	}
	return o
}

// verify checks a JWS body sent to url, answering its payload.
func (f *fakeACME) verify(r *http.Request) (payload []byte, err error) {
	var jws struct{ Protected, Payload, Signature string }
	if err := json.NewDecoder(r.Body).Decode(&jws); err != nil {
		return nil, err
	}
	header, err := base64.RawURLEncoding.DecodeString(jws.Protected)
	if err != nil {
		return nil, err
	}
	var protected struct {
		Alg, Nonce, URL, Kid string
		JWK                  json.RawMessage
	}
	if err := json.Unmarshal(header, &protected); err != nil {
		return nil, err
	}
	if protected.Alg != "ES256" || protected.URL != f.base+r.URL.Path {
		return nil, fmt.Errorf("bad header %s", header)
	}
	if !f.nonces[protected.Nonce] {
		return nil, fmt.Errorf("unknown nonce %q", protected.Nonce)
	}
	delete(f.nonces, protected.Nonce)
	if r.URL.Path == "/account" {
		var jwk struct{ Crv, Kty, X, Y string }
		if err := json.Unmarshal(protected.JWK, &jwk); err != nil {
			return nil, err
		}
		x, _ := base64.RawURLEncoding.DecodeString(jwk.X)
		y, _ := base64.RawURLEncoding.DecodeString(jwk.Y)
		f.key = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		f.jwk = string(protected.JWK)
	} else if protected.Kid != f.base+"/acct/1" {
		return nil, fmt.Errorf("unknown account %q", protected.Kid)
	}
	sig, err := base64.RawURLEncoding.DecodeString(jws.Signature)
	if err != nil || len(sig) != 64 {
		return nil, fmt.Errorf("bad signature")
	}
	digest := sha256.Sum256([]byte(jws.Protected + "." + jws.Payload))
	if !ecdsa.Verify(f.key, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, fmt.Errorf("signature does not verify")
	}
	return base64.RawURLEncoding.DecodeString(jws.Payload)
}

func (f *fakeACME) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/dir":
			f.reply(w, http.StatusOK, map[string]string{"newNonce": f.base + "/nonce", "newAccount": f.base + "/account", "newOrder": f.base + "/order"})
		case "/nonce":
			f.freshNonce(w)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
		return
	}
	if f.badNonces == 0 {
		f.badNonces++
		f.freshNonce(w)
		f.problem(w, http.StatusBadRequest, "badNonce", "try again")
		return
	}
	payload, err := f.verify(r)
	f.freshNonce(w)
	if err != nil {
		f.problem(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	switch {
	case r.URL.Path == "/account":
		var account struct{ Contact []string }
		_ = json.Unmarshal(payload, &account)
		f.contact = account.Contact
		w.Header().Set("Location", f.base+"/acct/1")
		f.reply(w, http.StatusCreated, map[string]string{"status": "valid"})
	case r.URL.Path == "/order" && len(payload) > 0:
		var order struct {
			Identifiers []struct{ Type, Value string }
		}
		_ = json.Unmarshal(payload, &order)
		for _, id := range order.Identifiers {
			f.domains = append(f.domains, id.Value)
		}
		w.Header().Set("Location", f.base+"/order/1")
		f.reply(w, http.StatusCreated, f.order())
	case r.URL.Path == "/order/1":
		f.polls++
		if f.orderState == "pending" && len(f.accepted) == len(f.domains) && f.polls > 1 {
			f.orderState = "ready"
		} else if f.orderState == "processing" {
			f.orderState = "valid"
		}
		f.reply(w, http.StatusOK, f.order())
	case strings.HasPrefix(r.URL.Path, "/authz/"):
		var i int
		_, _ = fmt.Sscanf(r.URL.Path, "/authz/%d", &i)
		f.reply(w, http.StatusOK, map[string]any{
			"identifier": map[string]string{"type": "dns", "value": f.domains[i]},
			"status":     "pending",
			"challenges": []map[string]string{
				{"type": "http-01", "url": fmt.Sprintf("%s/chall/%d", f.base, i), "token": fmt.Sprintf("token-%d", i)},
				{"type": "dns-01", "url": fmt.Sprintf("%s/chall/%d", f.base, i), "token": fmt.Sprintf("token-%d", i)},
			},
		})
	case strings.HasPrefix(r.URL.Path, "/chall/"):
		var i int
		_, _ = fmt.Sscanf(r.URL.Path, "/chall/%d", &i)
		f.accepted[i] = true
		f.reply(w, http.StatusOK, map[string]string{"type": "http-01", "status": "processing"})
	case r.URL.Path == "/finalize":
		var finalize struct{ CSR string }
		_ = json.Unmarshal(payload, &finalize)
		der, _ := base64.RawURLEncoding.DecodeString(finalize.CSR)
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil || csr.CheckSignature() != nil || f.orderState != "ready" {
			f.problem(w, http.StatusBadRequest, "badCSR", fmt.Sprint(err))
			return
		}
		f.csrNames = csr.DNSNames
		template := &x509.Certificate{
			SerialNumber: big.NewInt(42),
			Subject:      csr.Subject,
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			DNSNames:     csr.DNSNames,
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		f.issued, err = x509.CreateCertificate(rand.Reader, template, f.ca, csr.PublicKey, crypto.Signer(f.caKey))
		require.NoError(f.t, err)
		f.orderState = "processing"
		f.reply(w, http.StatusOK, f.order())
	case r.URL.Path == "/cert" && f.issued != nil:
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_ = pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: f.issued})
		_ = pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: f.ca.Raw})
	default:
		f.problem(w, http.StatusNotFound, "malformed", "no such resource "+r.URL.Path)
	}
}
