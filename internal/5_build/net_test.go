package build_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSelfSigned writes a self-signed certificate for localhost and
// 127.0.0.1, and its key, as PEM files in dir.
func writeSelfSigned(t *testing.T, dir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
}

// TestNet runs TCP, UDP and TLS conversations over loopback between
// threads of one program, and checks addresses, refusals and timeouts.
func TestNet(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "zamnet")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	writeSelfSigned(t, dir)

	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("io/net/*");

namespace Main {
    static fun int(Socket.Listener*) echo = (l) {
        var Pair[Socket*, Err*] a = l.Accept();
        if(a.second != null) {
            return -1;
        }
        var Socket* c = a.first;
        var byte* buf = new(byte, 64U);
        var int total = 0;
        for(;;) {
            var Pair[int, Err*] r = c.Read(buf, 64);
            if(r.second != null || r.first == 0) {
                break;
            }
            c.Write(buf, r.first);
            total = total + r.first;
        }
        free(buf);
        c.Close();
        return total;
    }

    static fun int(Socket.Listener*, TLS.Context*) tlsServe = (l, ctx) {
        var Pair[Socket*, Err*] a = l.Accept();
        if(a.second != null) {
            return -1;
        }
        var Pair[TLS*, Err*] t = type(TLS).Server(a.first, ctx);
        if(t.second != null) {
            a.first.Close();
            return 0;
        }
        var byte* buf = new(byte, 64U);
        var Pair[int, Err*] r = t.first.Read(buf, 64);
        t.first.WriteStr("re:" + type(Strs).FromBytes(buf, r.first));
        free(buf);
        t.first.Close();
        return 1;
    }

    static fun str(Pair[TLS*, Err*]) tlsTry = (t) {
        if(t.second != null) {
            return "rejected";
        }
        t.first.WriteStr("hi");
        var str reply = t.first.ReadAll().first;
        t.first.Close();
        return reply;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str dir = argV[0];
        var IP six = type(IP).Parse("0:0:0:0:0:0:0:1").first;
        var str s = six.Str() + " " + six.IsV6(){str} + " " + six.IsLoopback(){str} + " " + type(IP).Parse("10.1.2.3").first.IsLoopback(){str};
        s = s + " " + (type(IP).Parse("nope").second != null){str};
        var Arr[IP]* local = type(IP).Lookup("localhost").first;
        var bool loop = local.Len() > 0U;
        for(var uint i = 0U; i < local.Len(); i++) {
            loop = loop && local.Get(i).IsLoopback();
        }
        s = s + " " + loop{str} + "\n";

        var Socket.Listener* l = type(TCP).Listen("127.0.0.1", 0U).first;
        var uint port = l.Addr().first.second;
        var thread[int] server = async(type(Main).echo(l));
        var Socket* c = type(TCP).Dial("127.0.0.1", port).first;
        var Pair[IP, uint] peer = c.RemoteAddr().first;
        c.WriteStr("hello tcp");
        c.CloseWrite();
        s = s + c.ReadAll().first + " " + (peer.second == port){str} + " " + peer.first.Str();
        c.Close();
        s = s + " " + wait(server){str} + " " + l.Close(){str} + " " + l.Close(){str} + " " + l.Accept().second.Err();
        s = s + " " + (type(TCP).Dial("127.0.0.1", port).second != null){str} + "\n";

        var UDP.Conn* ua = type(UDP).Bind("127.0.0.1", 0U).first;
        var UDP.Conn* ub = type(UDP).Bind("127.0.0.1", 0U).first;
        var uint aport = ua.LocalAddr().first.second;
        var Pair[byte*, int] ping = type(Strs).ToBytes("ping");
        ub.SendTo(ping.first, 4U, type(IP).Parse("127.0.0.1").first, aport);
        var byte* got = new(byte, 16U);
        var Pair[UDP.Packet, Err*] packet = ua.RecvFrom(got, 16U);
        s = s + type(Strs).FromBytes(got, packet.first.len{int}) + " " + (packet.first.port == ub.LocalAddr().first.second){str} + " " + packet.first.ip.Str() + "\n";
        free(got);
        ua.Close();
        ub.Close();

        var TLS.Context* sctx = type(TLS.Context).Server(type(FS).Join(dir, "cert.pem"), type(FS).Join(dir, "key.pem")).first;
        var TLS.Context* trusting = type(TLS.Context).Client(type(FS).Join(dir, "cert.pem"), true).first;
        var TLS.Context* system = type(TLS.Context).Client("", true).first;
        var TLS.Context* lax = type(TLS.Context).Client("", false).first;
        var Socket.Listener* tl = type(TCP).Listen("127.0.0.1", 0U).first;
        var uint tport = tl.Addr().first.second;
        var thread[int] ts = async(type(Main).tlsServe(tl, sctx));
        s = s + type(Main).tlsTry(type(TLS).Client(type(TCP).Dial("127.0.0.1", tport).first, trusting, "localhost")) + " " + wait(ts){str};
        ts = async(type(Main).tlsServe(tl, sctx));
        s = s + " " + type(Main).tlsTry(type(TLS).Client(type(TCP).Dial("127.0.0.1", tport).first, trusting, "127.0.0.1")) + " " + wait(ts){str};
        ts = async(type(Main).tlsServe(tl, sctx));
        s = s + " " + type(Main).tlsTry(type(TLS).Client(type(TCP).Dial("127.0.0.1", tport).first, trusting, "example.com")) + " " + wait(ts){str};
        ts = async(type(Main).tlsServe(tl, sctx));
        s = s + " " + type(Main).tlsTry(type(TLS).Client(type(TCP).Dial("127.0.0.1", tport).first, system, "localhost")) + " " + wait(ts){str};
        ts = async(type(Main).tlsServe(tl, sctx));
        s = s + " " + type(Main).tlsTry(type(TLS).Client(type(TCP).Dial("127.0.0.1", tport).first, lax, "example.com")) + " " + wait(ts){str};
        tl.Close();
        s = s + " " + (type(TLS.Context).Server("missing.pem", "missing.pem").second != null){str} + "\n";

        var Socket.Listener* sl = type(TCP).Listen("127.0.0.1", 0U).first;
        var Socket* quiet = type(TCP).Dial("127.0.0.1", sl.Addr().first.second).first;
        var Socket* held = sl.Accept().first;
        quiet.SetReadTimeout(100U);
        var byte* one = new(byte, 1U);
        s = s + (quiet.Read(one, 1).second != null){str} + "\n";
        free(one);
        quiet.Close();
        held.Close();
        sl.Close();
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, dir)
	require.Zero(t, status, stderr)
	assert.Equal(t, "::1 true true false true true\n"+
		"hello tcp true 127.0.0.1 9 true false listener closed true\n"+
		"ping true 127.0.0.1\n"+
		"re:hi 1 re:hi 1 rejected 0 rejected 0 re:hi 1 true\n"+
		"true\n", stdout)
}

// TestUnixSocket echoes over a Unix socket, and checks the listener removes
// its path on closing. It is skipped where the environment forbids binding
// Unix sockets, as some sandboxes do.
func TestUnixSocket(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "zamunix")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	probe, err := net.Listen("unix", filepath.Join(dir, "probe.sock"))
	if err != nil {
		t.Skipf("cannot bind a Unix socket here: %v", err)
	}
	require.NoError(t, probe.Close())

	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("io/net/*");

namespace Main {
    static fun int(Socket.Listener*) echo = (l) {
        var Socket* c = l.Accept().first;
        var str all = c.ReadAll().first;
        c.WriteStr(all);
        c.Close();
        return type(Strs).ByteLen(all);
    }

    fun void(uint, str...) main = (argC, argV) {
        var str path = type(FS).Join(argV[0], "echo.sock");
        var Socket.Listener* l = type(UnixSocket).Listen(path).first;
        var thread[int] server = async(type(Main).echo(l));
        var Socket* c = type(UnixSocket).Dial(path).first;
        var str peer = c.RemotePath().first;
        c.WriteStr("hello unix");
        c.CloseWrite();
        var str s = c.ReadAll().first + " " + wait(server){str} + " " + peer;
        c.Close();
        s = s + " " + (c.RemoteAddr().second != null){str} + " " + l.Close(){str} + " " + type(FS).Exists(path){str};
        s = s + " " + (type(UnixSocket).Dial(path).second != null){str} + "\n";
        type(File).StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, dir)
	require.Zero(t, status, stderr)
	assert.Equal(t, "hello unix 10 "+filepath.Join(dir, "echo.sock")+" true true false true\n", stdout)
}
