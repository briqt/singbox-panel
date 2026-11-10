package singbox

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/briqt/singbox-panel/model"
)

// 用真实内核证伪授权和出口，不把 check 成功当作实际转发成功。
// 全程使用回环地址及临时进程，不触碰正在运行的节点服务。
func TestCoreInboundUpstream(t *testing.T) {
	bin := os.Getenv("SINGBOX_TEST_BIN")
	if bin == "" {
		t.Skip("set SINGBOX_TEST_BIN to run the real-core integration test")
	}
	for _, scenario := range []string{"authorized", "wrong-upstream-password", "unauthorized-user"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			var originHits, proxyHits atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originHits.Add(1)
				io.WriteString(w, "upstream-only")
			}))
			defer origin.Close()
			handshake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
			handshake.EnableHTTP2 = true
			handshake.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
			handshake.StartTLS()
			defer handshake.Close()
			handshakePort := handshake.Listener.Addr().(*net.TCPAddr).Port
			socksPort := coreSOCKSServer(t, &proxyHits)
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			serverPort, clientPort := freeTCPPort(t), freeTCPPort(t)
			settings, _ := json.Marshal(map[string]any{"sni": "localhost", "handshake_server": "127.0.0.1", "handshake_port": handshakePort, "private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "short_id": "abcd"})
			password := "proxy-password"
			if scenario == "wrong-upstream-password" {
				password += "-wrong"
			}
			uuid := "11111111-1111-4111-8111-111111111111"
			ib := model.NodeInbound{ID: 1, Tag: "reality-upstream", Protocol: "vless-reality", Port: serverPort, Enabled: true, Settings: settings,
				Outbound: &model.OutboundConfig{Type: "socks", Server: "127.0.0.1", ServerPort: socksPort, Username: "proxy-user", Password: password}}
			data, err := GenerateConfigForInbounds(map[int][]model.User{1: {{Name: "allowed", UUID: uuid}}}, []model.NodeInbound{ib})
			server := decodeGenerated(t, data, err)
			delete(server, "experimental")
			for _, inbound := range server["inbounds"].([]any) {
				inbound.(map[string]any)["listen"] = "127.0.0.1"
			}
			startCore(t, bin, dir, "server", server)
			waitCoreTCP(t, serverPort)
			if scenario == "unauthorized-user" {
				uuid = "22222222-2222-4222-8222-222222222222"
			}
			startCore(t, bin, dir, "client", map[string]any{
				"inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": clientPort}},
				"outbounds": []any{map[string]any{"type": "vless", "server": "127.0.0.1", "server_port": serverPort, "uuid": uuid, "flow": "xtls-rprx-vision", "tls": map[string]any{
					"enabled": true, "server_name": "localhost", "utls": map[string]any{"enabled": true, "fingerprint": "chrome"},
					"reality": map[string]any{"enabled": true, "public_key": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "short_id": "abcd"},
				}}},
			})
			waitCoreTCP(t, clientPort)
			proxy, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(clientPort))
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			response, err := client.Get(origin.URL)
			if scenario != "authorized" {
				if response != nil {
					response.Body.Close()
				}
				if err == nil && response.StatusCode == 200 || originHits.Load() != 0 {
					t.Fatalf("应拒绝访问且不可回落直连：err=%v origin_hits=%d", err, originHits.Load())
				}
				if scenario == "wrong-upstream-password" && proxyHits.Load() == 0 {
					t.Fatal("证伪必须实际触达上游，不能因入站未启动而通过")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != 200 || string(body) != "upstream-only" || proxyHits.Load() == 0 || originHits.Load() != 1 {
				t.Fatalf("实际 Reality→SOCKS5 转发未得到验证：status=%d proxy_hits=%d origin_hits=%d", response.StatusCode, proxyHits.Load(), originHits.Load())
			}
		})
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func waitCoreTCP(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("测试内核未开始监听 %d", port)
}

func coreSOCKSServer(t *testing.T, attempts *atomic.Int32) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				var header [2]byte
				if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 5 {
					return
				}
				if _, err := io.CopyN(io.Discard, conn, int64(header[1])); err != nil {
					return
				}
				conn.Write([]byte{5, 2})
				if _, err := io.ReadFull(conn, header[:]); err != nil {
					return
				}
				username := make([]byte, int(header[1]))
				if _, err := io.ReadFull(conn, username); err != nil {
					return
				}
				var length [1]byte
				if _, err := io.ReadFull(conn, length[:]); err != nil {
					return
				}
				password := make([]byte, int(length[0]))
				if _, err := io.ReadFull(conn, password); err != nil {
					return
				}
				attempts.Add(1)
				if string(username) != "proxy-user" || string(password) != "proxy-password" {
					conn.Write([]byte{1, 1})
					return
				}
				conn.Write([]byte{1, 0})
				var request [4]byte
				if _, err := io.ReadFull(conn, request[:]); err != nil || request[0] != 5 || request[1] != 1 {
					return
				}
				var host string
				switch request[3] {
				case 1:
					var ip [4]byte
					if _, err := io.ReadFull(conn, ip[:]); err != nil {
						return
					}
					host = net.IP(ip[:]).String()
				case 3:
					if _, err := io.ReadFull(conn, length[:]); err != nil {
						return
					}
					domain := make([]byte, int(length[0]))
					if _, err := io.ReadFull(conn, domain); err != nil {
						return
					}
					host = string(domain)
				default:
					return
				}
				var port [2]byte
				if _, err := io.ReadFull(conn, port[:]); err != nil {
					return
				}
				upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				go io.Copy(upstream, conn)
				io.Copy(conn, upstream)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}
