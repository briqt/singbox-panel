package model

import (
	"strings"
	"testing"

	paneldb "github.com/briqt/singbox-panel/db"
)

func TestCreateNodeDefaultsToCurrentLayout(t *testing.T) {
	database, err := paneldb.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	node, err := (&NodeStore{DB: database}).Create(CreateNodeReq{Name: "new", Host: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	if node.SingboxBin != DefaultSingboxBin {
		t.Fatalf("singbox_bin=%q want %q", node.SingboxBin, DefaultSingboxBin)
	}
	if node.ConfigPath != DefaultConfigPath {
		t.Fatalf("config_path=%q want %q", node.ConfigPath, DefaultConfigPath)
	}
	if strings.Contains(node.SingboxBin, "v2ray-agent") || strings.Contains(node.ConfigPath, "v2ray-agent") {
		t.Fatal("new nodes still default to the v2ray-agent tree")
	}
}
