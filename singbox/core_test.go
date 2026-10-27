package singbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/briqt/singbox-panel/model"
)

// Optional integration test against the exact release binary, not a schema
// approximation. Run via scripts/check_core.sh on a Linux test host.
func TestCoreHysteriaProfiles(t *testing.T) {
	bin := os.Getenv("SINGBOX_TEST_BIN")
	if bin == "" {
		t.Skip("set SINGBOX_TEST_BIN to run the real-core integration test")
	}
	for _, profile := range []string{"quic", "salamander"} {
		t.Run(profile, func(t *testing.T) {
			dir := t.TempDir()
			certPath, keyPath := coreTestCertificate(t, dir)
			udp, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			serverPort := udp.LocalAddr().(*net.UDPAddr).Port
			udp.Close()
			password := "11111111-1111-4111-8111-111111111111"
			settings, _ := json.Marshal(map[string]any{
				"profile": profile, "domain": "localhost", "cert_path": certPath, "key_path": keyPath,
				"obfs_password": "retained-secret",
			})
			config, err := GenerateConfig([]model.User{{Name: "test", UUID: password}}, []model.NodeInbound{{
				Protocol: "hysteria2", Tag: "hy2", Port: serverPort, Enabled: true, Settings: settings,
			}})
			if err != nil {
				t.Fatal(err)
			}
			fullConfig := filepath.Join(dir, "full.json")
			if err := os.WriteFile(fullConfig, config, 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bin, "check", "-c", fullConfig).CombinedOutput(); err != nil {
				t.Fatalf("core rejects generated config: %v %s", err, out)
			}
			// Existing services on the test host may own the panel's default
			// API ports. Runtime testing needs only the generated protocol.
			var server map[string]any
			json.Unmarshal(config, &server)
			delete(server, "experimental")
			startCore(t, bin, dir, "server", server)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tunnel-ok") }))
			defer origin.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			clientPort := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			outbound := map[string]any{
				"type": "hysteria2", "server": "127.0.0.1", "server_port": serverPort, "password": password,
				"tls": map[string]any{"enabled": true, "server_name": "localhost", "certificate_path": certPath},
			}
			if profile == "salamander" {
				outbound["obfs"] = map[string]any{"type": "salamander", "password": "retained-secret"}
			}
			startCore(t, bin, dir, "client", map[string]any{
				"inbounds":  []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": clientPort}},
				"outbounds": []any{outbound},
			})
			proxy, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(clientPort))
			transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			deadline := time.Now().Add(10 * time.Second)
			for {
				response, err := client.Get(origin.URL)
				if err == nil {
					body, _ := io.ReadAll(response.Body)
					response.Body.Close()
					if response.StatusCode == 200 && string(body) == "tunnel-ok" {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("real %s tunnel failed: %v", profile, err)
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
}

func startCore(t *testing.T, bin, dir, name string, config map[string]any) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(dir, name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "-c", path)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		logFile.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logFile.Name())
			t.Logf("%s: %s", name, data)
		}
	})
}

func coreTestCertificate(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
