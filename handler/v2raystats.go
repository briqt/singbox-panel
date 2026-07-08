package handler

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/encoding/protowire"
)

// v2ray_api exposes the V2Ray StatsService. sing-box reuses the upstream
// service name and message layout, so we speak to it directly with a tiny
// hand-rolled protobuf codec instead of pulling in generated stubs.
const (
	statsServiceQueryStats = "/v2ray.core.app.stats.command.StatsService/QueryStats"
	statsUserPrefix        = "user>>>"
)

// UserTraffic is a per-user uplink/downlink delta read from a node.
type UserTraffic struct {
	Up   int64
	Down int64
}

// queryUserStats dials the node's v2ray_api over the given SSH connection and
// returns per-user traffic keyed by user name. With reset=true each call
// returns the delta accumulated since the previous call, so the poller does
// not need to track baselines itself.
func queryUserStats(sshClient *ssh.Client, reset bool) (map[string]UserTraffic, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(
		"passthrough:///v2ray-api",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return sshClient.Dial("tcp", statsListenAddr)
		}),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(statsCodec{})),
	)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := &queryStatsRequest{pattern: statsUserPrefix, reset_: reset}
	resp := &queryStatsResponse{}
	if err := conn.Invoke(ctx, statsServiceQueryStats, req, resp); err != nil {
		return nil, err
	}

	result := make(map[string]UserTraffic)
	for _, s := range resp.stats {
		name, kind, ok := parseUserStatName(s.name)
		if !ok {
			continue
		}
		t := result[name]
		switch kind {
		case "uplink":
			t.Up += s.value
		case "downlink":
			t.Down += s.value
		}
		result[name] = t
	}
	return result, nil
}

// statsListenAddr mirrors singbox.V2RayAPIListen; kept as a package-level var so
// it stays a single source of truth without importing the singbox package here.
var statsListenAddr = "127.0.0.1:10085"

// parseUserStatName splits "user>>>NAME>>>traffic>>>uplink" into the user name
// and the uplink/downlink kind.
func parseUserStatName(name string) (user, kind string, ok bool) {
	if !strings.HasPrefix(name, statsUserPrefix) {
		return "", "", false
	}
	parts := strings.Split(name, ">>>")
	if len(parts) != 4 || parts[2] != "traffic" {
		return "", "", false
	}
	return parts[1], parts[3], true
}

// --- minimal protobuf messages + codec for the StatsService ---

type queryStatsRequest struct {
	pattern string
	reset_  bool
}

type stat struct {
	name  string
	value int64
}

type queryStatsResponse struct {
	stats []stat
}

type statsCodec struct{}

func (statsCodec) Name() string { return "proto" }

func (statsCodec) Marshal(v any) ([]byte, error) {
	req, ok := v.(*queryStatsRequest)
	if !ok {
		return nil, fmt.Errorf("statsCodec: unexpected request type %T", v)
	}
	var b []byte
	if req.pattern != "" {
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendString(b, req.pattern)
	}
	if req.reset_ {
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, 1)
	}
	return b, nil
}

func (statsCodec) Unmarshal(data []byte, v any) error {
	resp, ok := v.(*queryStatsResponse)
	if !ok {
		return fmt.Errorf("statsCodec: unexpected response type %T", v)
	}
	// QueryStatsResponse { repeated Stat stat = 1; }
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		if num != 1 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			continue
		}
		msg, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		s, err := parseStat(msg)
		if err != nil {
			return err
		}
		resp.stats = append(resp.stats, s)
	}
	return nil
}

// parseStat decodes Stat { string name = 1; int64 value = 2; }
func parseStat(data []byte) (stat, error) {
	var s stat
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return s, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			name, n := protowire.ConsumeString(data)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			s.name = name
			data = data[n:]
		case num == 2 && typ == protowire.VarintType:
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			s.value = int64(val)
			data = data[n:]
		default:
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return s, protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return s, nil
}

var _ encoding.Codec = statsCodec{}
