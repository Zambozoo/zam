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
        var URL* u = type(URL).Parse("https://user:pw@example.com:8443/a/b%20c?x=1&y=two+words&z=%26&flag#frag").first;
        var str s = u.scheme + "|" + u.user + "|" + u.password + "|" + u.host + "|" + u.port{str} + "|" + u.path + "|" + u.query + "|" + u.fragment + "\n";
        s = s + u.QueryValue("y").first + "|" + u.QueryValue("z").first + "|" + u.QueryValue("flag").second{str} + "|" + u.QueryValue("nope").second{str} + "|" + u.QueryValues().Len(){str} + "\n";
        s = s + u.Str() + "\n";
        s = s + u.Resolve("../c?q=1").first.Str() + " " + u.Resolve("//other.org/x").first.Str() + " " + u.Resolve("#top").first.Str() + "\n";
        u.path = "/new";
        u.query = "";
        u.port = 0U;
        s = s + u.Str() + "\n";
        s = s + type(URL).Escape("a b&c/é") + " " + type(URL).Unescape("a%20b%26c%2F%C3%A9") + " " + (type(URL).Parse("not a url").second != null){str};
        s = s + " " + type(URL).Parse("http://h").first.path + " " + type(URL).Parse("http://h").first.port{str} + "\n";
        type(File).StdOut.WriteStr(s);
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
        var HTTP.Response* r = type(HTTP).Get(base + "/echo?a=1").first;
        var str s = r.status{str} + " " + r.ReadBody().first + " " + r.headers.Value("x-method").first + " " + r.headers.Value("Content-Length").first + "\n";

        var HTTP.Request* req = type(HTTP.Request).New("PUT", base + "/echo");
        req.headers.Set("X-Test", "hdr");
        req.SetBody("payload");
        r = type(HTTP).Do(req).first;
        s = s + r.ReadBody().first + " " + r.headers.Value("X-Multi").first + " " + r.headers.Values("x-multi"){str} + "\n";
        var HTTP.Headers* h = type(HTTP.Headers).New();
        h.Add("Accept", "a");
        h.Add("ACCEPT", "b");
        h.Set("X-One", "1");
        h.Set("x-one", "2");
        s = s + h.Values("accept"){str} + " " + h.Value("X-ONE").first + " " + h.Len(){str} + " " + h.Get("x-one").first{str};
        h.Del("Accept");
        s = s + " " + h.Contains("accept"){str} + " " + h.Values("accept").Len(){str} + "\n";

        r = type(HTTP).Post(base + "/echo", "text/plain", "posted").first;
        s = s + r.ReadBody().first + "\n";
        r = type(HTTP).Get(base + "/redirect").first;
        s = s + r.status{str} + " " + r.ReadBody().first + "\n";
        req = type(HTTP.Request).New("GET", base + "/redirect");
        req.followRedirects = false;
        r = type(HTTP).Do(req).first;
        s = s + r.status{str} + " " + r.headers.Value("Location").first + "\n";
        r = type(HTTP).Get(base + "/gzip").first;
        s = s + type(Strs).ByteLen(r.ReadBody().first){str} + " " + r.status{str} + "\n";
        r = type(HTTP).Get(base + "/teapot").first;
        s = s + r.status{str} + " " + r.ReadBody().first + " " + type(HTTP).StatusText(404U) + "\n";
        r = type(HTTP).Do(type(HTTP.Request).New("HEAD", base + "/echo")).first;
        s = s + r.status{str} + " [" + r.ReadBody().first + "]\n";
        s = s + (type(HTTP).Get("http://127.0.0.1:1/").second != null){str} + "\n";

        r = type(HTTP).Open(type(HTTP.Request).New("GET", base + "/stream")).first;
        var byte* chunk = new(byte, 4U);
        var int reads = 0;
        var str streamed = "";
        for(;;) {
            var Pair[int, Err*] p = r.body.Read(chunk, 4);
            if(p.second != null || p.first == 0) {
                break;
            }
            reads++;
            streamed = streamed + type(Strs).FromBytes(chunk, p.first);
        }
        s = s + r.status{str} + " " + streamed + " " + (reads >= 4){str} + " " + r.body.Read(chunk, 4).first{str} + "\n";
        free(chunk);

        var Buffer* big = type(Buffer).New();
        for(var int i = 0; i < 10000; i++) {
            big.WriteStr("0123456789");
        }
        req = type(HTTP.Request).New("POST", base + "/count");
        req.body = big;
        s = s + type(HTTP).Do(req).first.ReadBody().first + "\n";
        req = type(HTTP.Request).New("POST", base + "/count");
        req.SetBody("sized");
        r = type(HTTP).Open(req).first;
        s = s + r.ReadBody().first + " " + (r.body.Close() == null){str} + "\n";
        type(File).StdOut.WriteStr(s);
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
            var Arr[Pair[str, str]]* q = r.QueryValues();
            if(q.Len() > 0U) {
                name = q.Get(0U).second;
            }
            var HTTP.Response* resp = type(HTTP.Response).New(200U, "hello " + name);
            resp.headers.Set("Content-Type", "text/plain");
            return resp;
        }
        if(path == "/echo") {
            return type(HTTP.Response).New(201U, r.method + " " + r.headers.Value("x-test").first + " [" + r.ReadBody().first + "]");
        }
        if(path == "/upload") {
            var byte* b = new(byte, 1000U);
            var int total = 0;
            var int reads = 0;
            for(;;) {
                var Pair[int, Err*] p = r.body.Read(b, 1000);
                if(p.second != null || p.first == 0) {
                    break;
                }
                total = total + p.first;
                reads++;
            }
            free(b);
            return type(HTTP.Response).New(200U, total{str} + " " + r.length{str} + " " + (reads >= 100){str});
        }
        if(path == "/chunked") {
            var Buffer* b = type(Buffer).New();
            for(var int i = 0; i < 3000; i++) {
                b.WriteStr("chunk ");
            }
            return type(HTTP.Response).Of(200U, b, -1);
        }
        if(path == "/ws") {
            var Pair[WebSocket*, Err*] a = type(WebSocket).Accept(r);
            if(a.second != null) {
                return type(HTTP.Response).New(400U, a.second.Err());
            }
            var WebSocket* ws = a.first;
            for(;;) {
                var WebSocket.Message* m = ws.Receive().first;
                if(m.closed) {
                    break;
                }
                if(m.text) {
                    ws.SendText("echo " + m.data);
                } else {
                    var Pair[byte*, int] bytes = type(Strs).ToBytes(m.data);
                    ws.SendBinary(bytes.first, bytes.second{uint});
                }
            }
            return null;
        }
        if(path == "/null") {
            return null;
        }
        if(path == "/quit") {
            type(Main).server.Close();
            return type(HTTP.Response).New(200U, "bye");
        }
        return type(HTTP.Response).New(404U, "no " + path);
    }

    fun void(uint, str...) main = (argC, argV) {
        type(Main).server = type(HTTP.Server).Listen("127.0.0.1", 0U, type(Main).handle).first;
        type(File).StdOut.WriteStr(type(Main).server.Addr().first.second{str} + "\n");
        type(Main).server.Serve();
        type(File).StdOut.WriteStr("stopped\n");
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
        return "binary:" + type(Strs).ByteLen(m.data){str};
    }

    static fun str(WebSocket*) waitClosed = (ws) {
        return type(Main).show(ws.Receive().first);
    }

    fun void(uint, str...) main = (argC, argV) {
        var str url = "ws" + argV[0] + "/ws";
        var WebSocket* ws = type(WebSocket).Dial(url, null, null).first;
        ws.SendText("hello");
        var str s = type(Main).show(ws.Receive().first);
        var byte* raw = new(byte, 70000U);
        for(var uint i = 0U; i < 70000U; i++) {
            raw[i] = (i % 256U){byte};
        }
        ws.SendBinary(raw, 70000U);
        free(raw);
        var WebSocket.Message* big = ws.Receive().first;
        var Pair[byte*, int] bytes = type(Strs).ToBytes(big.data);
        s = s + " " + type(Main).show(big) + " " + bytes.first[69999]{int}{str};
        ws.SendText("frag");
        s = s + " " + type(Main).show(ws.Receive().first);
        ws.SendText("bye");
        s = s + " " + type(Main).show(ws.Receive().first) + " " + (ws.SendText("after") != null){str} + "\n";

        var WebSocket* other = type(WebSocket).Dial(url, null, null).first;
        var thread[str] reader = async(type(Main).waitClosed(other));
        other.Close();
        s = s + wait(reader) + " " + (type(WebSocket).Dial("ws://127.0.0.1:1/ws", null, null).second != null){str} + "\n";
        type(File).StdOut.WriteStr(s);
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
            var Pair[WebSocket*, Err*] a = type(WebSocket).Accept(r);
            if(a.second != null) {
                return type(HTTP.Response).New(400U, a.second.Err());
            }
            var WebSocket* ws = a.first;
            for(;;) {
                var WebSocket.Message* m = ws.Receive().first;
                if(m.closed) {
                    break;
                }
                if(m.text) {
                    ws.SendText("echo " + m.data);
                } else {
                    var Pair[byte*, int] bytes = type(Strs).ToBytes(m.data);
                    ws.SendBinary(bytes.first, bytes.second{uint});
                }
            }
            return null;
        }
        if(path == "/quit") {
            type(Main).plain.Close();
            type(Main).secure.Close();
            return type(HTTP.Response).New(200U, "bye");
        }
        return type(HTTP.Response).New(200U, "secure " + r.ReadBody().first);
    }

    static fun int(HTTP.Server*) run = (s) {
        s.Serve();
        return 0;
    }

    static fun str(str, TLS.Context*) talk = (url, ctx) {
        var Pair[WebSocket*, Err*] d = type(WebSocket).Dial(url, null, ctx);
        if(d.second != null) {
            return d.second.Err();
        }
        var WebSocket* ws = d.first;
        ws.SendText("hi");
        var str s = ws.Receive().first.data;
        var byte* raw = new(byte, 70000U);
        for(var uint i = 0U; i < 70000U; i++) {
            raw[i] = (i % 251U){byte};
        }
        ws.SendBinary(raw, 70000U);
        free(raw);
        var WebSocket.Message* big = ws.Receive().first;
        var Pair[byte*, int] bytes = type(Strs).ToBytes(big.data);
        s = s + " " + big.text{str} + " " + bytes.second{str} + " " + bytes.first[69999]{int}{str};
        ws.Close();
        s = s + " " + ws.Receive().first.closed{str} + " " + (ws.SendText("late") != null){str};
        return s;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str dir = argV[0];
        var TLS.Context* server = type(TLS.Context).Server(dir + "/cert.pem", dir + "/key.pem").first;
        var TLS.Context* client = type(TLS.Context).Client(dir + "/cert.pem", true).first;
        type(Main).plain = type(HTTP.Server).Listen("127.0.0.1", 0U, type(Main).handle).first;
        type(Main).secure = type(HTTP.Server).ListenTLS("127.0.0.1", 0U, server, type(Main).handle).first;
        var thread[int] p = async(type(Main).run(type(Main).plain));
        var thread[int] q = async(type(Main).run(type(Main).secure));
        var str plainPort = type(Main).plain.Addr().first.second{str};
        var str securePort = type(Main).secure.Addr().first.second{str};
        var str s = type(Main).talk("ws://127.0.0.1:" + plainPort + "/ws", client) + "\n";
        s = s + type(Main).talk("wss://127.0.0.1:" + securePort + "/ws", client) + "\n";
        s = s + (type(WebSocket).Dial("ws://127.0.0.1:" + securePort + "/ws", null, null).second != null){str} + "\n";
        type(File).StdOut.WriteStr(securePort + "\n" + s);
        wait(p);
        wait(q);
        type(File).StdOut.WriteStr("stopped\n");
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
