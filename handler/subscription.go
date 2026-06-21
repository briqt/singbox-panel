package handler

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/briqt/singbox-panel/model"
	"github.com/briqt/singbox-panel/singbox"
)

type SubscriptionHandler struct {
	Users *model.UserStore
	Nodes *model.NodeStore
}

func (h *SubscriptionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/sub/")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	user, err := h.Users.GetBySubToken(token)
	if err != nil || !h.Users.IsActive(user) {
		http.NotFound(w, r)
		return
	}

	nodes, err := h.Nodes.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var nodesWithInbounds []model.NodeWithInbounds
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		inbounds, err := h.Nodes.ListInbounds(n.ID)
		if err != nil {
			continue
		}
		nodesWithInbounds = append(nodesWithInbounds, model.NodeWithInbounds{
			Node:     n,
			Inbounds: inbounds,
		})
	}

	content := singbox.GenerateSubscription(*user, nodesWithInbounds)
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	// Subscription-Userinfo header (used by Clash, Shadowrocket, etc.)
	upload := user.TrafficUsedBytes / 2
	download := user.TrafficUsedBytes / 2
	total := user.TrafficLimitBytes
	userinfo := fmt.Sprintf("upload=%d; download=%d; total=%d", upload, download, total)
	if user.ExpireAt != "" {
		userinfo += fmt.Sprintf("; expire=%s", user.ExpireAt)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Subscription-Userinfo", userinfo)
	w.Header().Set("Profile-Update-Interval", "6")
	w.Header().Set("Content-Disposition", "attachment; filename=nodes")
	w.Write([]byte(encoded))
}
