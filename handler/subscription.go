package handler

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/briqt/singbox-panel/model"
	"github.com/briqt/singbox-panel/singbox"
)

type SubscriptionHandler struct {
	Users  *model.UserStore
	Nodes  *model.NodeStore
	Access *model.AccessStore
}

const unlimitedSubscriptionTotalBytes int64 = 1 << 50 // 1 PiB display placeholder

func (h *SubscriptionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/sub/")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	user, err := h.Users.GetBySubToken(token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.Users.CheckTrafficReset(user)
	if !h.Users.IsActive(user) {
		http.NotFound(w, r)
		return
	}

	nodes, err := h.Nodes.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	accessibleInbounds, err := h.Access.ListInboundIDs(user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	accessSet := make(map[int]bool, len(accessibleInbounds))
	for _, id := range accessibleInbounds {
		accessSet[id] = true
	}
	accessibleNodes, err := h.Access.NodeIDsForInbounds(accessibleInbounds)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nodeSet := make(map[int]bool, len(accessibleNodes))
	for _, id := range accessibleNodes {
		nodeSet[id] = true
	}

	var nodesWithInbounds []model.NodeWithInbounds
	for _, n := range nodes {
		if !n.Enabled || !nodeSet[n.ID] {
			continue
		}
		inbounds, err := h.Nodes.ListInbounds(n.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var allowedInbounds []model.NodeInbound
		for _, inbound := range inbounds {
			if inbound.Enabled && accessSet[inbound.ID] {
				allowedInbounds = append(allowedInbounds, inbound)
			}
		}
		if len(allowedInbounds) > 0 {
			nodesWithInbounds = append(nodesWithInbounds, model.NodeWithInbounds{
				Node:     n,
				Inbounds: allowedInbounds,
			})
		}
	}

	// Subscription-Userinfo header
	upload := model.AccountedBytes(user.TrafficUpBytes)
	download := model.AccountedBytes(user.TrafficDownBytes)
	total := user.TrafficLimitBytes
	if total == 0 {
		total = unlimitedSubscriptionTotalBytes
	}
	userinfo := fmt.Sprintf("upload=%d; download=%d; total=%d", upload, download, total)
	if user.ExpireAt != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", user.ExpireAt); err == nil {
			userinfo += fmt.Sprintf("; expire=%d", t.Unix())
		}
	}
	w.Header().Set("Subscription-Userinfo", userinfo)
	w.Header().Set("Profile-Update-Interval", "6")

	format := detectFormat(r)
	switch format {
	case "clash":
		content := singbox.GenerateClashConfig(*user, nodesWithInbounds)
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=config.yaml")
		w.Write([]byte(content))
	default:
		content := singbox.GenerateSubscription(*user, nodesWithInbounds)
		encoded := base64.StdEncoding.EncodeToString([]byte(content))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=nodes")
		w.Write([]byte(encoded))
	}
}

func detectFormat(r *http.Request) string {
	if f := r.URL.Query().Get("format"); f != "" {
		return f
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "clash"):
		return "clash"
	case strings.Contains(ua, "mihomo"):
		return "clash"
	case strings.Contains(ua, "stash"):
		return "clash"
	case strings.Contains(ua, "verge"):
		return "clash"
	}
	return "base64"
}
