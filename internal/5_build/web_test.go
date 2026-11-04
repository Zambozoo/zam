package build_test

import (
	"bufio"
	"compress/gzip"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestURL parses, rebuilds, resolves and escapes URLs.
func TestURL(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("io/web/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var URL* u = URL::Parse("https://user:pw@example.com:8443/a/b%20c?x=1&y=two+words&z=%26&flag#frag").0;
        var str s = u.scheme + "|" + u.user + "|" + u.password + "|" + u.host + "|" + u.port{str} + "|" + u.path + "|" + u.query + "|" + u.fragment + "\n";
        s = s + u.QueryValue("y").0 + "|" + u.QueryValue("z").0 + "|" + u.QueryValue("flag").1{str} + "|" + u.QueryValue("nope").1{str} + "|" + u.QueryValues().Len(){str} + "\n";
        s = s + u.Str() + "\n";
        s = s + u.Resolve("../c?q=1").0.Str() + " " + u.Resolve("//other.org/x").0.Str() + " " + u.Resolve("#top").0.Str() + "\n";
        u.path = "/new";
        u.query = "";
        u.port = 0U;
        s = s + u.Str() + "\n";
        s = s + URL::Escape("a b&c/é") + " " + URL::Unescape("a%20b%26c%2F%C3%A9") + " " + (URL::Parse("not a url").1 != null){str};
        s = s + " " + URL::Parse("http://h").0.path + " " + URL::Parse("http://h").0.port{str} + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "https|user|pw|example.com|8443|/a/b%20c|x=1&y=two+words&z=%26&flag|frag\n"+
		"two words|&|true|false|4\n"+
		"https://user:pw@example.com:8443/a/b%20c?x=1&y=two+words&z=%26&flag#frag\n"+
		"https://user:pw@example.com:8443/c?q=1 https://other.org/x https://user:pw@example.com:8443/a/b%20c?x=1&y=two+words&z=%26&flag#top\n"+
		"https://user:pw@example.com/new#frag\n"+
		"a%20b%26c%2F%C3%A9 a b&c/é true / 0\n", stdout)
}

// TestHTTPClient sends requests from zam to a server Go's net/http runs.
func TestHTTPClient(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Method", r.Method)
		w.Header().Add("X-Multi", "one")
		w.Header().Add("X-Multi", "two")
		fmt.Fprintf(w, "%s %s %s [%s]", r.Method, r.URL.RequestURI(), r.Header.Get("X-Test"), body)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/echo?from=redirect", http.StatusFound)
	})
	mux.HandleFunc("/gzip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		io.WriteString(gz, strings.Repeat("squeeze ", 100))
		gz.Close()
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		for _, part := range []string{"one ", "two ", "three"} {
			io.WriteString(w, part)
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%v %d %d", r.TransferEncoding, r.ContentLength, len(body))
	})
	mux.HandleFunc("/teapot", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "short and stout")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("io/web/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var str base = argV[0];
        var HTTP.Response* r = HTTP::Get(base + "/echo?a=1").0;
        var str s = r.status{str} + " " + r.ReadBody().0 + " " + r.headers.Value("x-method").0 + " " + r.headers.Value("Content-Length").0 + "\n";

        var HTTP.Request* req = HTTP.Request::New("PUT", base + "/echo");
        req.headers.Set("X-Test", "hdr");
        req.SetBody("payload");
        r = HTTP::Do(req).0;
        s = s + r.ReadBody().0 + " " + r.headers.Value("X-Multi").0 + " " + r.headers.Values("x-multi"){str} + "\n";
        var HTTP.Headers* h = HTTP.Headers::New();
        h.Add("Accept", "a");
        h.Add("ACCEPT", "b");
        h.Set("X-One", "1");
        h.Set("x-one", "2");
        s = s + h.Values("accept"){str} + " " + h.Value("X-ONE").0 + " " + h.Len(){str} + " " + h.Get("x-one").0{str};
        h.Del("Accept");
        s = s + " " + h.Contains("accept"){str} + " " + h.Values("accept").Len(){str} + "\n";

        r = HTTP::Post(base + "/echo", "text/plain", "posted").0;
        s = s + r.ReadBody().0 + "\n";
        r = HTTP::Get(base + "/redirect").0;
        s = s + r.status{str} + " " + r.ReadBody().0 + "\n";
        req = HTTP.Request::New("GET", base + "/redirect");
        req.followRedirects = false;
        r = HTTP::Do(req).0;
        s = s + r.status{str} + " " + r.headers.Value("Location").0 + "\n";
        r = HTTP::Get(base + "/gzip").0;
        s = s + Strs::ByteLen(r.ReadBody().0){str} + " " + r.status{str} + "\n";
        r = HTTP::Get(base + "/teapot").0;
        s = s + r.status{str} + " " + r.ReadBody().0 + " " + HTTP::StatusText(404U) + "\n";
        r = HTTP::Do(HTTP.Request::New("HEAD", base + "/echo")).0;
        s = s + r.status{str} + " [" + r.ReadBody().0 + "]\n";
        s = s + (HTTP::Get("http://127.0.0.1:1/").1 != null){str} + "\n";

        r = HTTP::Open(HTTP.Request::New("GET", base + "/stream")).0;
        var byte* chunk = new(byte, 4U);
        var int reads = 0;
        var str streamed = "";
        for(;;) {
            var (int, Err*) p = r.body.Read(chunk, 4);
            if(p.1 != null || p.0 == 0) {
                break;
            }
            reads++;
            streamed = streamed + Strs::FromBytes(chunk, p.0);
        }
        s = s + r.status{str} + " " + streamed + " " + (reads >= 4){str} + " " + r.body.Read(chunk, 4).0{str} + "\n";
        free(chunk);

        var Buffer* big = Buffer::New();
        for(var int i = 0; i < 10000; i++) {
            big.WriteStr("0123456789");
        }
        req = HTTP.Request::New("POST", base + "/count");
        req.body = big;
        s = s + HTTP::Do(req).0.ReadBody().0 + "\n";
        req = HTTP.Request::New("POST", base + "/count");
        req.SetBody("sized");
        r = HTTP::Open(req).0;
        s = s + r.ReadBody().0 + " " + (r.body.Close() == null){str} + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, server.URL)
	require.Zero(t, status, stderr)
	assert.Equal(t, "200 GET /echo?a=1  [] GET 17\n"+
		"PUT /echo hdr [payload] one [one, two]\n"+
		"[a, b] 2 2 [2] false 0\n"+
		"POST /echo  [posted]\n"+
		"200 GET /echo?from=redirect  []\n"+
		"302 /echo?from=redirect\n"+
		"800 200\n"+
		"418 short and stout Not Found\n"+
		"200 []\n"+
		"true\n"+
		"200 one two three true 0\n"+
		"[chunked] -1 100000\n"+
		"[] 5 5 true\n", stdout)
}

const webServerProgram = `import std("lang/*");
import std("io/local/*");
import std("io/web/*");

namespace Main {
    static var HTTP.Server* server = null;

    static fun HTTP.Response*(HTTP.Request*) handle = (r) {
        var str path = r.Path();
        if(path == "/hello") {
            var str name = "world";
            var Arr[(str, str)]* q = r.QueryValues();
            if(q.Len() > 0U) {
                name = q.Get(0U).1;
            }
            var HTTP.Response* resp = HTTP.Response::New(200U, "hello " + name);
            resp.headers.Set("Content-Type", "text/plain");
            return resp;
        }
        if(path == "/echo") {
            return HTTP.Response::New(201U, r.method + " " + r.headers.Value("x-test").0 + " [" + r.ReadBody().0 + "]");
        }
        if(path == "/upload") {
            var byte* b = new(byte, 1000U);
            var int total = 0;
            var int reads = 0;
            for(;;) {
                var (int, Err*) p = r.body.Read(b, 1000);
                if(p.1 != null || p.0 == 0) {
                    break;
                }
                total = total + p.0;
                reads++;
            }
            free(b);
            return HTTP.Response::New(200U, total{str} + " " + r.length{str} + " " + (reads >= 100){str});
        }
        if(path == "/chunked") {
            var Buffer* b = Buffer::New();
            for(var int i = 0; i < 3000; i++) {
                b.WriteStr("chunk ");
            }
            return HTTP.Response::Of(200U, b, -1);
        }
        if(path == "/ws") {
            var (WebSocket*, Err*) a = WebSocket::Accept(r);
            if(a.1 != null) {
                return HTTP.Response::New(400U, a.1.Err());
            }
            var WebSocket* ws = a.0;
            for(;;) {
                var WebSocket.Message* m = ws.Receive().0;
                if(m.closed) {
                    break;
                }
                if(m.text) {
                    ws.SendText("echo " + m.data);
                } else {
                    var (byte*, int) bytes = Strs::ToBytes(m.data);
                    ws.SendBinary(bytes.0, bytes.1{uint});
                }
            }
            return null;
        }
        if(path == "/null") {
            return null;
        }
        if(path == "/quit") {
            Main::server.Close();
            return HTTP.Response::New(200U, "bye");
        }
        return HTTP.Response::New(404U, "no " + path);
    }

    fun void(uint, str...) main = (argC, argV) {
        Main::server = HTTP.Server::Listen("127.0.0.1", 0U, Main::handle).0;
        File::StdOut.WriteStr(Main::server.Addr().0.1{str} + "\n");
        Main::server.Serve();
        File::StdOut.WriteStr("stopped\n");
    }
}
`

// TestHTTPServer serves from zam to Go's net/http client and to a raw
// connection that pipelines requests, streaming bodies both ways.
func TestHTTPServer(t *testing.T) {
	t.Parallel()
	binary := compile(t, webServerProgram)
	cmd := exec.Command(binary)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { cmd.Process.Kill() })
	lines := bufio.NewReader(stdout)
	port, err := lines.ReadString('\n')
	require.NoError(t, err, stderr.String())
	base := "http://127.0.0.1:" + strings.TrimSpace(port)

	client := &http.Client{Timeout: 10 * time.Second}
	get := func(path string) (int, string, http.Header) {
		resp, err := client.Get(base + path)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body), resp.Header
	}

	code, body, header := get("/hello?name=zam+lang")
	assert.Equal(t, 200, code)
	assert.Equal(t, "hello zam lang", body)
	assert.Equal(t, "text/plain", header.Get("Content-Type"))
	code, body, _ = get("/hello")
	assert.Equal(t, "hello world", body)
	code, body, _ = get("/missing")
	assert.Equal(t, 404, code)
	assert.Equal(t, "no /missing", body)
	code, _, _ = get("/null")
	assert.Equal(t, 500, code)

	req, err := http.NewRequest("POST", base+"/echo", strings.NewReader("data"))
	require.NoError(t, err)
	req.Header.Set("X-Test", "hdr")
	resp, err := client.Do(req)
	require.NoError(t, err)
	posted, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 201, resp.StatusCode)
	assert.Equal(t, "POST hdr [data]", string(posted))

	upload := strings.Repeat("u", 200000)
	req, err = http.NewRequest("POST", base+"/upload", io.NopCloser(strings.NewReader(upload)))
	require.NoError(t, err)
	resp, err = client.Do(req)
	require.NoError(t, err)
	uploaded, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "200000 -1 true", string(uploaded))

	waiting := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{ExpectContinueTimeout: 5 * time.Second}}
	req, err = http.NewRequest("POST", base+"/upload", strings.NewReader(upload))
	require.NoError(t, err)
	req.Header.Set("Expect", "100-continue")
	start := time.Now()
	resp, err = waiting.Do(req)
	require.NoError(t, err)
	uploaded, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "200000 200000 true", string(uploaded))
	assert.Less(t, time.Since(start), 4*time.Second)

	resp, err = client.Get(base + "/chunked")
	require.NoError(t, err)
	chunked, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, []string{"chunked"}, resp.TransferEncoding)
	assert.Equal(t, strings.Repeat("chunk ", 3000), string(chunked))

	code, body, _ = get("/ws")
	assert.Equal(t, 400, code)
	assert.Equal(t, "not a WebSocket request", body)

	head, err := client.Head(base + "/hello")
	require.NoError(t, err)
	head.Body.Close()
	assert.Equal(t, int64(11), head.ContentLength)

	raw, err := net.Dial("tcp", "127.0.0.1:"+strings.TrimSpace(port))
	require.NoError(t, err)
	_, err = io.WriteString(raw, "GET /hello?n=a HTTP/1.1\r\nHost: x\r\n\r\nGET /hello?n=b HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	require.NoError(t, err)
	reader := bufio.NewReader(raw)
	for _, want := range []string{"hello a", "hello b"} {
		pipelined, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)
		got, _ := io.ReadAll(pipelined.Body)
		pipelined.Body.Close()
		assert.Equal(t, want, string(got))
	}
	raw.Close()

	bad, err := net.Dial("tcp", "127.0.0.1:"+strings.TrimSpace(port))
	require.NoError(t, err)
	io.WriteString(bad, "NOT HTTP AT ALL\r\n\r\n")
	badResp, err := http.ReadResponse(bufio.NewReader(bad), nil)
	require.NoError(t, err)
	assert.Equal(t, 400, badResp.StatusCode)
	bad.Close()

	client.Get(base + "/quit")
	rest, _ := io.ReadAll(lines)
	require.NoError(t, cmd.Wait(), stderr.String())
	assert.Equal(t, "stopped\n", string(rest))
}

// serveWebSocket is a minimal RFC 6455 echo server: it echoes each message,
// answers "frag" in two fragments, and closes on "bye".
func serveWebSocket() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
		rw.Flush()
		write := func(fin bool, opcode byte, payload []byte) {
			first := opcode
			if fin {
				first |= 0x80
			}
			frame := []byte{first}
			switch {
			case len(payload) < 126:
				frame = append(frame, byte(len(payload)))
			case len(payload) < 65536:
				frame = append(frame, 126, byte(len(payload)>>8), byte(len(payload)))
			default:
				frame = append(frame, 127)
				frame = binary.BigEndian.AppendUint64(frame, uint64(len(payload)))
			}
			rw.Write(append(frame, payload...))
			rw.Flush()
		}
		for {
			var head [2]byte
			if _, err := io.ReadFull(rw, head[:]); err != nil {
				return
			}
			opcode := head[0] & 0x0f
			length := uint64(head[1] & 0x7f)
			switch length {
			case 126:
				var ext [2]byte
				io.ReadFull(rw, ext[:])
				length = uint64(binary.BigEndian.Uint16(ext[:]))
			case 127:
				var ext [8]byte
				io.ReadFull(rw, ext[:])
				length = binary.BigEndian.Uint64(ext[:])
			}
			var mask [4]byte
			io.ReadFull(rw, mask[:])
			payload := make([]byte, length)
			if _, err := io.ReadFull(rw, payload); err != nil {
				return
			}
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
			switch {
			case opcode == 8:
				write(true, 8, nil)
				return
			case opcode == 9:
				write(true, 10, payload)
			case string(payload) == "frag":
				write(false, 1, []byte("ab"))
				write(true, 0, []byte("cd"))
			case string(payload) == "bye":
				write(true, 8, []byte{0x03, 0xe8})
				return
			default:
				write(true, opcode, payload)
			}
		}
	}))
}

// TestWebSocket talks to a Go WebSocket echo server from zam.
func TestWebSocket(t *testing.T) {
	t.Parallel()
	server := serveWebSocket()
	t.Cleanup(server.Close)

	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("io/web/*");

namespace Main {
    static fun str(WebSocket.Message*) show = (m) {
        if(m.closed) {
            return "closed";
        }
        if(m.text) {
            return "text:" + m.data;
        }
        return "binary:" + Strs::ByteLen(m.data){str};
    }

    static fun str(WebSocket*) waitClosed = (ws) {
        return Main::show(ws.Receive().0);
    }

    fun void(uint, str...) main = (argC, argV) {
        var str url = "ws" + argV[0] + "/ws";
        var WebSocket* ws = WebSocket::Dial(url, null, null).0;
        ws.SendText("hello");
        var str s = Main::show(ws.Receive().0);
        var byte* raw = new(byte, 70000U);
        for(var uint i = 0U; i < 70000U; i++) {
            raw[i] = (i % 256U){byte};
        }
        ws.SendBinary(raw, 70000U);
        free(raw);
        var WebSocket.Message* big = ws.Receive().0;
        var (byte*, int) bytes = Strs::ToBytes(big.data);
        s = s + " " + Main::show(big) + " " + bytes.0[69999]{int}{str};
        ws.SendText("frag");
        s = s + " " + Main::show(ws.Receive().0);
        ws.SendText("bye");
        s = s + " " + Main::show(ws.Receive().0) + " " + (ws.SendText("after") != null){str} + "\n";

        var WebSocket* other = WebSocket::Dial(url, null, null).0;
        var thread[str] reader = async(Main::waitClosed(other));
        other.Close();
        s = s + wait(reader) + " " + (WebSocket::Dial("ws://127.0.0.1:1/ws", null, null).1 != null){str} + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary, strings.TrimPrefix(server.URL, "http"))
	require.Zero(t, status, stderr)
	assert.Equal(t, "text:hello binary:70000 111 text:abcd closed true\nclosed true\n", stdout)
}

// TestHTTPSAndWebSocketServer serves HTTP and HTTPS from zam, opening
// WebSockets on both from zam's own client and answering Go's HTTPS client.
func TestHTTPSAndWebSocketServer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSelfSigned(t, dir)
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("io/net/*");
import std("io/web/*");

namespace Main {
    static var HTTP.Server* plain = null;
    static var HTTP.Server* secure = null;

    static fun HTTP.Response*(HTTP.Request*) handle = (r) {
        var str path = r.Path();
        if(path == "/ws") {
            var (WebSocket*, Err*) a = WebSocket::Accept(r);
            if(a.1 != null) {
                return HTTP.Response::New(400U, a.1.Err());
            }
            var WebSocket* ws = a.0;
            for(;;) {
                var WebSocket.Message* m = ws.Receive().0;
                if(m.closed) {
                    break;
                }
                if(m.text) {
                    ws.SendText("echo " + m.data);
                } else {
                    var (byte*, int) bytes = Strs::ToBytes(m.data);
                    ws.SendBinary(bytes.0, bytes.1{uint});
                }
            }
            return null;
        }
        if(path == "/quit") {
            Main::plain.Close();
            Main::secure.Close();
            return HTTP.Response::New(200U, "bye");
        }
        return HTTP.Response::New(200U, "secure " + r.ReadBody().0);
    }

    static fun int(HTTP.Server*) run = (s) {
        s.Serve();
        return 0;
    }

    static fun str(str, TLS.Context*) talk = (url, ctx) {
        var (WebSocket*, Err*) d = WebSocket::Dial(url, null, ctx);
        if(d.1 != null) {
            return d.1.Err();
        }
        var WebSocket* ws = d.0;
        ws.SendText("hi");
        var str s = ws.Receive().0.data;
        var byte* raw = new(byte, 70000U);
        for(var uint i = 0U; i < 70000U; i++) {
            raw[i] = (i % 251U){byte};
        }
        ws.SendBinary(raw, 70000U);
        free(raw);
        var WebSocket.Message* big = ws.Receive().0;
        var (byte*, int) bytes = Strs::ToBytes(big.data);
        s = s + " " + big.text{str} + " " + bytes.1{str} + " " + bytes.0[69999]{int}{str};
        ws.Close();
        s = s + " " + ws.Receive().0.closed{str} + " " + (ws.SendText("late") != null){str};
        return s;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str dir = argV[0];
        var TLS.Context* server = TLS.Context::Server(dir + "/cert.pem", dir + "/key.pem").0;
        var TLS.Context* client = TLS.Context::Client(dir + "/cert.pem", true).0;
        Main::plain = HTTP.Server::Listen("127.0.0.1", 0U, Main::handle).0;
        Main::secure = HTTP.Server::ListenTLS("127.0.0.1", 0U, server, Main::handle).0;
        var thread[int] p = async(Main::run(Main::plain));
        var thread[int] q = async(Main::run(Main::secure));
        var str plainPort = Main::plain.Addr().0.1{str};
        var str securePort = Main::secure.Addr().0.1{str};
        var str s = Main::talk("ws://127.0.0.1:" + plainPort + "/ws", client) + "\n";
        s = s + Main::talk("wss://127.0.0.1:" + securePort + "/ws", client) + "\n";
        s = s + (WebSocket::Dial("ws://127.0.0.1:" + securePort + "/ws", null, null).1 != null){str} + "\n";
        File::StdOut.WriteStr(securePort + "\n" + s);
        wait(p);
        wait(q);
        File::StdOut.WriteStr("stopped\n");
    }
}
`)
	cmd := exec.Command(binary, dir)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { cmd.Process.Kill() })
	lines := bufio.NewReader(stdout)
	port, err := lines.ReadString('\n')
	require.NoError(t, err, stderr.String())
	for _, want := range []string{"echo hi false 70000 221 true true\n", "echo hi false 70000 221 true true\n", "true\n"} {
		got, err := lines.ReadString('\n')
		require.NoError(t, err, stderr.String())
		assert.Equal(t, want, got)
	}

	pem, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pem))
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	base := "https://127.0.0.1:" + strings.TrimSpace(port)
	resp, err := client.Post(base+"/hello", "text/plain", strings.NewReader("data"))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "secure data", string(body))

	resp, err = client.Get(base + "/quit")
	require.NoError(t, err)
	resp.Body.Close()
	rest, _ := io.ReadAll(lines)
	require.NoError(t, cmd.Wait(), stderr.String())
	assert.Equal(t, "stopped\n", string(rest))
}
