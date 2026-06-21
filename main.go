package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/briqt/singbox-panel/config"
	"github.com/briqt/singbox-panel/db"
	"github.com/briqt/singbox-panel/handler"
	"github.com/briqt/singbox-panel/model"
)

func main() {
	cfg := config.Load()

	database, err := db.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	userStore := &model.UserStore{DB: database}
	nodeStore := &model.NodeStore{DB: database}

	userHandler := &handler.UserHandler{Store: userStore}
	nodeHandler := &handler.NodeHandler{Store: nodeStore}
	subHandler := &handler.SubscriptionHandler{Users: userStore, Nodes: nodeStore}
	configHandler := &handler.ConfigHandler{Users: userStore, Nodes: nodeStore, SSHKeyPath: cfg.SSHKeyPath}
	batchHandler := &handler.BatchHandler{Users: userStore, Nodes: nodeStore, Config: configHandler}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// User CRUD + batch ops
	mux.HandleFunc("/api/users", adminAuth(cfg.AdminToken, userHandler.ServeHTTP))
	mux.HandleFunc("/api/users/", adminAuth(cfg.AdminToken, userHandler.ServeHTTP))

	// Node CRUD + config ops
	mux.HandleFunc("/api/nodes", adminAuth(cfg.AdminToken, nodeHandler.ServeHTTP))
	mux.HandleFunc("/api/nodes/", adminAuth(cfg.AdminToken, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/generate") || strings.HasSuffix(path, "/push") ||
			strings.HasSuffix(path, "/raw-config") {
			configHandler.ServeHTTP(w, r)
		} else {
			nodeHandler.ServeHTTP(w, r)
		}
	}))
	mux.HandleFunc("/api/inbounds/", adminAuth(cfg.AdminToken, nodeHandler.ServeHTTP))

	// Batch operations
	mux.HandleFunc("/api/batch/push-all", adminAuth(cfg.AdminToken, batchHandler.PushAll))
	mux.HandleFunc("/api/batch/template", adminAuth(cfg.AdminToken, batchHandler.ApplyTemplate))

	// Stats
	mux.HandleFunc("/api/stats/users", adminAuth(cfg.AdminToken, configHandler.HandleUserStats))
	mux.HandleFunc("/api/stats/nodes", adminAuth(cfg.AdminToken, configHandler.HandleNodeStats))

	// Traffic report from node agents (auth via X-Node-Token)
	mux.HandleFunc("/api/node/report", configHandler.HandleTrafficReport)

	// Subscription (public, token in URL)
	mux.HandleFunc("/sub/", subHandler.ServeHTTP)

	addr := "127.0.0.1:" + cfg.Port
	log.Printf("singbox-panel listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func adminAuth(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next(w, r)
	}
}
