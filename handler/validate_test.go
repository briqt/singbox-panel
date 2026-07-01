package handler

import (
	"strings"
	"testing"
)

func TestBuildCertInstallScriptUsesLetsEncryptAndFailsFast(t *testing.T) {
	script := buildCertInstallScript(
		"/etc/sing-box/tls",
		"node.example.com",
		"/etc/sing-box/tls/node.example.com.crt",
		"/etc/sing-box/tls/node.example.com.key",
	)

	setDefault := strings.Index(script, "--set-default-ca --server letsencrypt")
	issue := strings.Index(script, "--issue -d node.example.com")
	if setDefault < 0 {
		t.Fatal("certificate script does not select Let's Encrypt")
	}
	if issue < 0 {
		t.Fatal("certificate script does not issue the requested domain")
	}
	if setDefault > issue {
		t.Fatal("CA must be selected before issuing the certificate")
	}
	if strings.Contains(script, "--force 2>&1 || true") {
		t.Fatal("certificate issuance errors must stop the script")
	}
}
