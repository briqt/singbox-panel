package model

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/briqt/singbox-panel/db"
)

func TestOutboundValidationRejectsInvalidValues(t *testing.T) {
	valid := OutboundConfig{Type: "socks", Server: "192.0.2.1", ServerPort: 443, Username: "name", Password: "secret", Network: "tcp"}
	if err := ValidateOutbound(&valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*OutboundConfig){
		func(c *OutboundConfig) { c.Type = "direct" },
		func(c *OutboundConfig) { c.Server = "example.com:443" },
		func(c *OutboundConfig) { c.Server = "https://example.com" },
		func(c *OutboundConfig) { c.ServerPort = 0 },
		func(c *OutboundConfig) { c.Network = "udp" },
		func(c *OutboundConfig) { c.Password = "" },
	} {
		broken := valid
		mutate(&broken)
		if err := ValidateOutbound(&broken); err == nil {
			t.Fatalf("无效配置未拒绝：type=%s server=%s port=%d network=%s", broken.Type, broken.Server, broken.ServerPort, broken.Network)
		}
	}
}

func TestInboundMetadataSurvivesUpdateRestoreAndDeletion(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NodeStore{DB: database}
	node, err := store.Create(CreateNodeReq{Name: "test", Host: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	req := CreateInboundReq{Tag: "custom", Name: "东京-上游", Protocol: "vless-reality", Port: 2053, Custom: true, Settings: json.RawMessage(`{"sni":"example.com"}`), Outbound: &OutboundConfig{Type: "socks", Server: "192.0.2.2", ServerPort: 443}}
	created, err := store.CreateInbound(node.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListInbounds(node.ID)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], *created) {
		t.Fatalf("新元数据读写不一致：%v %v", listed, err)
	}
	req.Name = "东京-专用"
	updated, err := store.UpdateInbound(created.ID, req)
	if err != nil || updated.Name != req.Name || !updated.Custom || !reflect.DeepEqual(updated.Outbound, req.Outbound) {
		t.Fatalf("更新丢失新元数据：%v %v", updated, err)
	}
	user, err := (&UserStore{DB: database}).Create(CreateUserReq{Name: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO user_inbound_access(user_id,inbound_id) VALUES(?,?)`, user.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteInbound(created.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM user_inbound_access WHERE inbound_id=?`, created.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("删除入站残留授权：%d %v", count, err)
	}
	restored, err := store.RestoreInbound(*updated)
	if err != nil || !reflect.DeepEqual(restored, updated) {
		t.Fatalf("回滚丢失元数据：%v %v", restored, err)
	}
}
