package model

import (
	"database/sql"
	"reflect"
	"testing"

	paneldb "github.com/briqt/singbox-panel/db"
)

type inboundAccessFixture struct {
	database *sql.DB
	users    *UserStore
	nodes    *NodeStore
	access   *AccessStore
	user     *User
	node     *Node
	first    *NodeInbound
	second   *NodeInbound
}

func newInboundAccessFixture(t *testing.T) *inboundAccessFixture {
	t.Helper()
	database, err := paneldb.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &inboundAccessFixture{
		database: database,
		users:    &UserStore{DB: database},
		nodes:    &NodeStore{DB: database},
		access:   &AccessStore{DB: database},
	}
	f.user, err = f.users.Create(CreateUserReq{Name: "authorized", UUID: "stable-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	f.node, err = f.nodes.Create(CreateNodeReq{Name: "node", Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	f.first = f.createInbound(t, "vless-reality", 443)
	f.second = f.createInbound(t, "vless-httpupgrade", 8443)
	return f
}

func (f *inboundAccessFixture) createInbound(t *testing.T, protocol string, port int) *NodeInbound {
	t.Helper()
	inbound, err := f.nodes.CreateInbound(f.node.ID, CreateInboundReq{Tag: protocol, Protocol: protocol, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	return inbound
}

func assertExactInboundAccess(t *testing.T, access *AccessStore, userID int, want []int) {
	t.Helper()
	got, err := access.ListInboundIDs(userID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inbound grants=%v want=%v", got, want)
	}
}

func TestLegacyNodeAccessAPIsAreExplicitInboundSnapshots(t *testing.T) {
	for _, method := range []string{"Grant", "GrantAll", "Replace", "UpdateWithAccess"} {
		t.Run(method, func(t *testing.T) {
			f := newInboundAccessFixture(t)
			grant := func() error {
				switch method {
				case "Grant":
					return f.access.Grant(f.user.ID, f.node.ID)
				case "GrantAll":
					return f.access.GrantAll(f.user.ID)
				case "Replace":
					return f.access.Replace(f.user.ID, []int{f.node.ID})
				default:
					_, err := f.users.UpdateWithAccess(f.user.ID, UpdateUserReq{}, []int{f.node.ID})
					return err
				}
			}
			if err := grant(); err != nil {
				t.Fatal(err)
			}
			assertExactInboundAccess(t, f.access, f.user.ID, []int{f.first.ID, f.second.ID})
			future := f.createInbound(t, "hysteria2", 9443)
			assertExactInboundAccess(t, f.access, f.user.ID, []int{f.first.ID, f.second.ID})
			if ids, err := f.access.UsersForInbound(future.ID); err != nil || len(ids) != 0 {
				t.Fatalf("future inbound inherited access: ids=%v err=%v", ids, err)
			}
			if err := grant(); err != nil {
				t.Fatal(err)
			}
			assertExactInboundAccess(t, f.access, f.user.ID, []int{f.first.ID, f.second.ID, future.ID})
		})
	}
}

func TestGrantNodeWithoutInboundsDoesNotGrantFutureInbounds(t *testing.T) {
	f := newInboundAccessFixture(t)
	empty, err := f.nodes.Create(CreateNodeReq{Name: "empty", Host: "127.0.0.2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.access.Grant(f.user.ID, empty.ID); err != nil {
		t.Fatal(err)
	}
	inbound, err := f.nodes.CreateInbound(empty.ID, CreateInboundReq{Protocol: "vless-httpupgrade", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	assertExactInboundAccess(t, f.access, f.user.ID, []int{})
	ids, err := f.access.UsersForInbound(inbound.ID)
	if err != nil || len(ids) != 0 {
		t.Fatalf("grant to empty node was inherited: ids=%v err=%v", ids, err)
	}
}

func TestInboundAccessNeverFallsBackToLegacyNodeGrants(t *testing.T) {
	f := newInboundAccessFixture(t)
	if _, err := f.database.Exec(`INSERT INTO user_access (user_id, node_id) VALUES (?, ?)`, f.user.ID, f.node.ID); err != nil {
		t.Fatal(err)
	}
	assertExactInboundAccess(t, f.access, f.user.ID, []int{})
	if nodes, err := f.access.ListNodeIDs(f.user.ID); err != nil || len(nodes) != 0 {
		t.Fatalf("legacy node grant became effective: nodes=%v err=%v", nodes, err)
	}
	if ids, err := f.access.UsersForInbound(f.first.ID); err != nil || len(ids) != 0 {
		t.Fatalf("legacy grant reached inbound: users=%v err=%v", ids, err)
	}
	if f.access.HasAccess(f.user.ID, f.node.ID) {
		t.Fatal("legacy grant passed node informational access check")
	}
	if err := f.access.ReplaceInbounds(f.user.ID, []int{f.first.ID}); err != nil {
		t.Fatal(err)
	}
	if nodes, err := f.access.ListNodeIDs(f.user.ID); err != nil || !reflect.DeepEqual(nodes, []int{f.node.ID}) {
		t.Fatalf("nodes must be derived from inbound grants: nodes=%v err=%v", nodes, err)
	}
	if err := f.access.RevokeAll(f.user.ID); err != nil {
		t.Fatal(err)
	}
	assertExactInboundAccess(t, f.access, f.user.ID, []int{})
	if count, err := f.access.CountForNode(f.node.ID); err != nil || count != 0 {
		t.Fatalf("legacy grant counted after revoke: count=%d err=%v", count, err)
	}
}

func TestUsersForInboundFiltersProtocolAndInactiveUsers(t *testing.T) {
	f := newInboundAccessFixture(t)
	if err := f.access.ReplaceInbounds(f.user.ID, []int{f.first.ID}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"disabled", "expired", "exhausted", "other-protocol"} {
		user, err := f.users.Create(CreateUserReq{Name: state})
		if err != nil {
			t.Fatal(err)
		}
		inboundID := f.first.ID
		switch state {
		case "disabled":
			enabled := false
			_, err = f.users.Update(user.ID, UpdateUserReq{Enabled: &enabled})
		case "expired":
			expireAt := "2000-01-01 00:00:00"
			_, err = f.users.Update(user.ID, UpdateUserReq{ExpireAt: &expireAt})
		case "exhausted":
			limit := int64(1)
			_, err = f.users.Update(user.ID, UpdateUserReq{TrafficLimitBytes: &limit})
			f.users.AddTraffic(user.ID, 1, 0)
		case "other-protocol":
			inboundID = f.second.ID
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := f.access.GrantInbound(user.ID, inboundID); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := f.access.UsersForInbound(f.first.ID)
	if err != nil || !reflect.DeepEqual(ids, []int{f.user.ID}) {
		t.Fatalf("first protocol received wrong users: ids=%v err=%v", ids, err)
	}
	count, err := f.access.CountForNode(f.node.ID)
	if err != nil || count != 5 {
		t.Fatalf("node count should count unique assigned users: count=%d err=%v", count, err)
	}
}

func TestUpdateWithInboundAccessRejectsInvalidGrantAtomically(t *testing.T) {
	f := newInboundAccessFixture(t)
	if err := f.access.ReplaceInbounds(f.user.ID, []int{f.first.ID}); err != nil {
		t.Fatal(err)
	}
	name := "changed"
	if _, err := f.users.UpdateWithInboundAccess(f.user.ID, UpdateUserReq{Name: &name}, []int{f.second.ID, 99999}); err == nil {
		t.Fatal("invalid inbound accepted")
	}
	assertExactInboundAccess(t, f.access, f.user.ID, []int{f.first.ID})
	user, err := f.users.Get(f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if user.Name != f.user.Name || user.UUID != f.user.UUID || user.SubToken != f.user.SubToken {
		t.Fatalf("failed transaction altered user: before=%+v after=%+v", f.user, user)
	}
	if err := f.access.ReplaceInbounds(f.user.ID, []int{f.second.ID, -1}); err == nil {
		t.Fatal("negative inbound ID silently accepted")
	}
	assertExactInboundAccess(t, f.access, f.user.ID, []int{f.first.ID})
}

func TestDeleteUserCleansExactInboundGrantsWithoutForeignKeyPragma(t *testing.T) {
	f := newInboundAccessFixture(t)
	if err := f.access.ReplaceInbounds(f.user.ID, []int{f.first.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.users.DeleteWithRelatedData(f.user.ID); err != nil {
		t.Fatal(err)
	}
	assertExactInboundAccess(t, f.access, f.user.ID, []int{})
}
