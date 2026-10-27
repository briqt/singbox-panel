package handler

import "testing"

func TestParseListeningSockets(t *testing.T) {
	sockets := parseListeningSockets(`udp UNCONN 0 0 *:19566 *:*
tcp LISTEN 0 4096 [::]:27495 [::]:*
tcp LISTEN 0 4096 127.0.0.1:9090 0.0.0.0:*`)
	if !sockets["udp"][19566] {
		t.Fatal("missing UDP listener")
	}
	if !sockets["tcp"][27495] || !sockets["tcp"][9090] {
		t.Fatal("missing TCP listener")
	}
	if sockets["tcp"][19566] || sockets["udp"][27495] {
		t.Fatal("listener network was classified incorrectly")
	}
}
