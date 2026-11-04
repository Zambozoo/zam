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
        var (Socket*, Err*) a = l.Accept();
        if(a.1 != null) {
            return -1;
        }
        var Socket* c = a.0;
        var byte* buf = new(byte, 64U);
        var int total = 0;
        for(;;) {
            var (int, Err*) r = c.Read(buf, 64);
            if(r.1 != null || r.0 == 0) {
                break;
            }
            c.Write(buf, r.0);
            total = total + r.0;
        }
        free(buf);
        c.Close();
        return total;
    }

    static fun int(Socket.Listener*, TLS.Context*) tlsServe = (l, ctx) {
        var (Socket*, Err*) a = l.Accept();
        if(a.1 != null) {
            return -1;
        }
        var (TLS*, Err*) t = TLS::Server(a.0, ctx);
        if(t.1 != null) {
            a.0.Close();
            return 0;
        }
        var byte* buf = new(byte, 64U);
        var (int, Err*) r = t.0.Read(buf, 64);
        t.0.WriteStr("re:" + Strs::FromBytes(buf, r.0));
        free(buf);
        t.0.Close();
        return 1;
    }

    static fun str((TLS*, Err*)) tlsTry = (t) {
        if(t.1 != null) {
            return "rejected";
        }
        t.0.WriteStr("hi");
        var str reply = t.0.ReadAll().0;
        t.0.Close();
        return reply;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str dir = argV[0];
        var IP six = IP::Parse("0:0:0:0:0:0:0:1").0;
        var str s = six.Str() + " " + six.IsV6(){str} + " " + six.IsLoopback(){str} + " " + IP::Parse("10.1.2.3").0.IsLoopback(){str};
        s = s + " " + (IP::Parse("nope").1 != null){str};
        var Arr[IP]* local = IP::Lookup("localhost").0;
        var bool loop = local.Len() > 0U;
        for(var uint i = 0U; i < local.Len(); i++) {
            loop = loop && local.Get(i).IsLoopback();
        }
        s = s + " " + loop{str} + "\n";

        var Socket.Listener* l = TCP::Listen("127.0.0.1", 0U).0;
        var uint port = l.Addr().0.1;
        var thread[int] server = async(Main::echo(l));
        var Socket* c = TCP::Dial("127.0.0.1", port).0;
        var (IP, uint) peer = c.RemoteAddr().0;
        c.WriteStr("hello tcp");
        c.CloseWrite();
        s = s + c.ReadAll().0 + " " + (peer.1 == port){str} + " " + peer.0.Str();
        c.Close();
        s = s + " " + wait(server){str} + " " + l.Close(){str} + " " + l.Close(){str} + " " + l.Accept().1.Err();
        s = s + " " + (TCP::Dial("127.0.0.1", port).1 != null){str} + "\n";

        var UDP.Conn* ua = UDP::Bind("127.0.0.1", 0U).0;
        var UDP.Conn* ub = UDP::Bind("127.0.0.1", 0U).0;
        var uint aport = ua.LocalAddr().0.1;
        var (byte*, int) ping = Strs::ToBytes("ping");
        ub.SendTo(ping.0, 4U, IP::Parse("127.0.0.1").0, aport);
        var byte* got = new(byte, 16U);
        var (UDP.Packet, Err*) packet = ua.RecvFrom(got, 16U);
        s = s + Strs::FromBytes(got, packet.0.len{int}) + " " + (packet.0.port == ub.LocalAddr().0.1){str} + " " + packet.0.ip.Str() + "\n";
        free(got);
        ua.Close();
        ub.Close();

        var TLS.Context* sctx = TLS.Context::Server(FS::Join(dir, "cert.pem"), FS::Join(dir, "key.pem")).0;
        var TLS.Context* trusting = TLS.Context::Client(FS::Join(dir, "cert.pem"), true).0;
        var TLS.Context* system = TLS.Context::Client("", true).0;
        var TLS.Context* lax = TLS.Context::Client("", false).0;
        var Socket.Listener* tl = TCP::Listen("127.0.0.1", 0U).0;
        var uint tport = tl.Addr().0.1;
        var thread[int] ts = async(Main::tlsServe(tl, sctx));
        s = s + Main::tlsTry(TLS::Client(TCP::Dial("127.0.0.1", tport).0, trusting, "localhost")) + " " + wait(ts){str};
        ts = async(Main::tlsServe(tl, sctx));
        s = s + " " + Main::tlsTry(TLS::Client(TCP::Dial("127.0.0.1", tport).0, trusting, "127.0.0.1")) + " " + wait(ts){str};
        ts = async(Main::tlsServe(tl, sctx));
        s = s + " " + Main::tlsTry(TLS::Client(TCP::Dial("127.0.0.1", tport).0, trusting, "example.com")) + " " + wait(ts){str};
        ts = async(Main::tlsServe(tl, sctx));
        s = s + " " + Main::tlsTry(TLS::Client(TCP::Dial("127.0.0.1", tport).0, system, "localhost")) + " " + wait(ts){str};
        ts = async(Main::tlsServe(tl, sctx));
        s = s + " " + Main::tlsTry(TLS::Client(TCP::Dial("127.0.0.1", tport).0, lax, "example.com")) + " " + wait(ts){str};
        tl.Close();
        s = s + " " + (TLS.Context::Server("missing.pem", "missing.pem").1 != null){str} + "\n";

        var Socket.Listener* sl = TCP::Listen("127.0.0.1", 0U).0;
        var Socket* quiet = TCP::Dial("127.0.0.1", sl.Addr().0.1).0;
        var Socket* held = sl.Accept().0;
        quiet.SetReadTimeout(100U);
        var byte* one = new(byte, 1U);
        s = s + (quiet.Read(one, 1).1 != null){str} + "\n";
        free(one);
        quiet.Close();
        held.Close();
        sl.Close();
        File::StdOut.WriteStr(s);
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
        var Socket* c = l.Accept().0;
        var str all = c.ReadAll().0;
        c.WriteStr(all);
        c.Close();
        return Strs::ByteLen(all);
    }

    fun void(uint, str...) main = (argC, argV) {
        var str path = FS::Join(argV[0], "echo.sock");
        var Socket.Listener* l = UnixSocket::Listen(path).0;
        var thread[int] server = async(Main::echo(l));
        var Socket* c = UnixSocket::Dial(path).0;
        var str peer = c.RemotePath().0;
        c.WriteStr("hello unix");
        c.CloseWrite();
        var str s = c.ReadAll().0 + " " + wait(server){str} + " " + peer;
        c.Close();
        s = s + " " + (c.RemoteAddr().1 != null){str} + " " + l.Close(){str} + " " + FS::Exists(path){str};
        s = s + " " + (UnixSocket::Dial(path).1 != null){str} + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, dir)
	require.Zero(t, status, stderr)
	assert.Equal(t, "hello unix 10 "+filepath.Join(dir, "echo.sock")+" true true false true\n", stdout)
}
