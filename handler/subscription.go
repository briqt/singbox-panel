package handler

import (
	"encoding/base64"
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
	if err != nil || !user.Enabled {
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

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=nodes")
	w.Write([]byte(encoded))
}
