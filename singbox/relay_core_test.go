package singbox

import (
	"bytes"
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

// 用真实内核证伪"落地不通就失败"：入口 Reality → SS2022 → 落地 → 源站。
// 源站只监听回环，入口机若擅自直出也能直接打到它，所以失败场景下
// 源站命中数必须为 0；只看客户端报错不够，回落直连同样可能"看起来"失败。
func TestCoreRelayLandingFailsClosed(t *testing.T) {
	bin := os.Getenv("SINGBOX_TEST_BIN")
	if bin == "" {
		t.Skip("set SINGBOX_TEST_BIN to run the real-core integration test")
	}
	scenarios := []struct {
		name       string
		udp        bool
		wrongKey   bool
		denySource bool
		landingOff bool
		viaCounter bool
	}{
		{name: "tcp-ok", viaCounter: true},
		{name: "tcp-wrong-key", wrongKey: true, viaCounter: true},
		{name: "tcp-source-denied", denySource: true, viaCounter: true},
		{name: "tcp-landing-down", landingOff: true},
		{name: "udp-ok", udp: true},
		{name: "udp-wrong-key", udp: true, wrongKey: true},
		{name: "udp-source-denied", udp: true, denySource: true},
		{name: "udp-landing-down", udp: true, landingOff: true},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			dir := t.TempDir()
			var originHits, udpOriginHits, landingConns atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originHits.Add(1)
				io.WriteString(w, "via-landing")
			}))
			defer origin.Close()
			udpOrigin := udpEchoServer(t, &udpOriginHits)

			landingKey := randomSS2022Key(t)
			sources := []string{"127.0.0.1/32"}
			if sc.denySource {
				sources = []string{"192.0.2.1/32"}
			}
			landingPort := freeTCPPort(t)
			if !sc.landingOff {
				relaySettings, _ := json.Marshal(map[string]any{"method": model.DefaultSS2022Method, "password": landingKey, "allowed_sources": sources})
				data, err := GenerateConfigForInbounds(nil, []model.NodeInbound{{ID: 9, Tag: "relay", Protocol: RelayProtocol, Port: landingPort, Enabled: true, Custom: true, Settings: relaySettings}})
				landing := decodeGenerated(t, data, err)
				delete(landing, "experimental")
				landing["inbounds"].([]any)[0].(map[string]any)["listen"] = "127.0.0.1"
				startCore(t, bin, dir, "landing", landing)
				waitCoreTCP(t, landingPort)
			}
			upstreamPort := landingPort
			if sc.viaCounter {
				upstreamPort = countingTCPForwarder(t, landingPort, &landingConns)
			}
			entryKey := landingKey
			if sc.wrongKey {
				entryKey = randomSS2022Key(t)
			}

			handshake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
			handshake.EnableHTTP2 = true
			handshake.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
			handshake.StartTLS()
			defer handshake.Close()
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			entryPort, clientPort := freeTCPPort(t), freeTCPPort(t)
			settings, _ := json.Marshal(map[string]any{"sni": "localhost", "handshake_server": "127.0.0.1", "handshake_port": handshake.Listener.Addr().(*net.TCPAddr).Port, "private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "short_id": "abcd"})
			uuid := "11111111-1111-4111-8111-111111111111"
			entryInbound := model.NodeInbound{ID: 1, Tag: "vless-reality-laxcg", Protocol: "vless-reality", Port: entryPort, Enabled: true, Custom: true, Settings: settings,
				Outbound: &model.OutboundConfig{Type: "shadowsocks", Server: "127.0.0.1", ServerPort: upstreamPort, Method: model.DefaultSS2022Method, Password: entryKey}}
			data, err := GenerateConfigForInbounds(map[int][]model.User{1: {{Name: "u", UUID: uuid}}}, []model.NodeInbound{entryInbound})
			entry := decodeGenerated(t, data, err)
			delete(entry, "experimental")
			entry["inbounds"].([]any)[0].(map[string]any)["listen"] = "127.0.0.1"
			startCore(t, bin, dir, "entry", entry)
			waitCoreTCP(t, entryPort)
			startCore(t, bin, dir, "client", map[string]any{
				"inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": clientPort}},
				"outbounds": []any{map[string]any{"type": "vless", "server": "127.0.0.1", "server_port": entryPort, "uuid": uuid, "flow": "xtls-rprx-vision", "packet_encoding": "xudp", "tls": map[string]any{
					"enabled": true, "server_name": "localhost", "utls": map[string]any{"enabled": true, "fingerprint": "chrome"},
					"reality": map[string]any{"enabled": true, "public_key": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "short_id": "abcd"},
				}}},
			})
			waitCoreTCP(t, clientPort)

			ok := !sc.wrongKey && !sc.denySource && !sc.landingOff
			if sc.udp {
				reply, err := socks5UDPExchange(clientPort, udpOrigin, []byte("ping-udp"))
				if ok {
					if err != nil || string(reply) != "ping-udp" || udpOriginHits.Load() == 0 {
						t.Fatalf("UDP 经落地转发未得到验证：err=%v reply=%q hits=%d", err, reply, udpOriginHits.Load())
					}
					return
				}
				if err == nil || udpOriginHits.Load() != 0 {
					t.Fatalf("落地不可用时 UDP 必须失败且不可直出：err=%v origin_hits=%d", err, udpOriginHits.Load())
				}
				return
			}

			proxy, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(clientPort))
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
			response, err := client.Get(origin.URL)
			if ok {
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, _ := io.ReadAll(response.Body)
				if response.StatusCode != 200 || string(body) != "via-landing" || originHits.Load() != 1 || landingConns.Load() == 0 {
					t.Fatalf("TCP 经落地转发未得到验证：status=%d origin_hits=%d landing_conns=%d", response.StatusCode, originHits.Load(), landingConns.Load())
				}
				return
			}
			if response != nil {
				response.Body.Close()
			}
			if err == nil && response.StatusCode == 200 || originHits.Load() != 0 {
				t.Fatalf("落地不可用时必须失败且不可直出：err=%v origin_hits=%d", err, originHits.Load())
			}
			if sc.viaCounter && landingConns.Load() == 0 {
				t.Fatal("证伪必须实际触达落地，不能因入口未启动而通过")
			}
		})
	}
}

func randomSS2022Key(t *testing.T) string {
	t.Helper()
	key := make([]byte, model.SS2022Methods[model.DefaultSS2022Method])
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// countingTCPForwarder 夹在入口与落地之间，证明失败场景确实触达了落地。
func countingTCPForwarder(t *testing.T, target int, conns *atomic.Int32) int {
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
			conns.Add(1)
			go func() {
				defer conn.Close()
				upstream, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(target)), time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				go io.Copy(upstream, conn)
				io.Copy(conn, upstream)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func udpEchoServer(t *testing.T, hits *atomic.Int32) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			hits.Add(1)
			conn.WriteTo(buf[:n], addr)
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// socks5UDPExchange 经本地 mixed 入站做一次 SOCKS5 UDP ASSOCIATE 往返。
func socks5UDPExchange(proxyPort, targetPort int, payload []byte) ([]byte, error) {
	control, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)), time.Second)
	if err != nil {
		return nil, err
	}
	defer control.Close()
	control.SetDeadline(time.Now().Add(6 * time.Second))
	if _, err := control.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(control, reply); err != nil || reply[1] != 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(control, head); err != nil || head[1] != 0 || head[3] != 1 {
		return nil, io.ErrUnexpectedEOF
	}
	bound := make([]byte, 6)
	if _, err := io.ReadFull(control, bound); err != nil {
		return nil, err
	}
	relay := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(binary.BigEndian.Uint16(bound[4:]))}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	packet := []byte{0, 0, 0, 1, 127, 0, 0, 1, byte(targetPort >> 8), byte(targetPort)}
	packet = append(packet, payload...)
	buf := make([]byte, 2048)
	// UDP 无连接，首包可能在链路建立前就被丢掉，给几次机会再判定失败。
	for attempt := 0; attempt < 4; attempt++ {
		if _, err := conn.WriteToUDP(packet, relay); err != nil {
			return nil, err
		}
		conn.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if n < 10 {
			return nil, io.ErrUnexpectedEOF
		}
		return bytes.Clone(buf[10:n]), nil
	}
	return nil, os.ErrDeadlineExceeded
}
