package main

import (
	_ "embed"
	"log"
	"net/http"
	"strings"

	"github.com/briqt/singbox-panel/config"
	"github.com/briqt/singbox-panel/db"
	"github.com/briqt/singbox-panel/handler"
	"github.com/briqt/singbox-panel/model"
)

//go:embed web/index.html
var adminHTML []byte

func main() {
	cfg := config.Load()

	database, err := db.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	userStore := &model.UserStore{DB: database}
	nodeStore := &model.NodeStore{DB: database}
	accessStore := &model.AccessStore{DB: database}

	authHandler := &handler.AuthHandler{
		Users: userStore, AdminUser: cfg.AdminUser,
		AdminPass: cfg.AdminPass, JWTSecret: cfg.JWTSecret,
	}
	meHandler := &handler.MeHandler{Users: userStore, Nodes: nodeStore, Access: accessStore}
	subHandler := &handler.SubscriptionHandler{Users: userStore, Nodes: nodeStore, Access: accessStore}
	configHandler := &handler.ConfigHandler{Users: userStore, Nodes: nodeStore, Access: accessStore, SSHKeyPath: cfg.SSHKeyPath}
	batchHandler := &handler.BatchHandler{Nodes: nodeStore, Config: configHandler}
	userHandler := &handler.UserHandler{Store: userStore, Access: accessStore, Sync: configHandler}
	accessHandler := &handler.AccessHandler{Access: accessStore, Nodes: nodeStore, Sync: configHandler}
	nodeHandler := &handler.NodeHandler{Store: nodeStore, Access: accessStore, Sync: configHandler}
	nodeOpsHandler := &handler.NodeOpsHandler{Nodes: nodeStore, Config: configHandler}
	setupHandler := &handler.SetupHandler{Nodes: nodeStore, Config: configHandler, Ops: nodeOpsHandler}
	validateHandler := &handler.ValidateHandler{Config: configHandler}

	trafficPoller := &handler.TrafficPoller{Nodes: nodeStore, Users: userStore, Config: configHandler}
	trafficPoller.Start()

	admin := authHandler.AdminOnly
	auth := authHandler.JWTAuth

	mux := http.NewServeMux()

	mux.HandleFunc("/admin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(adminHTML)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(adminHTML)
			return
		}
		http.NotFound(w, r)
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Public
	mux.HandleFunc("/api/login", authHandler.HandleLogin)
	mux.HandleFunc("/api/register", authHandler.HandleRegister)

	// User self-service (any authenticated user)
	mux.HandleFunc("/api/me", auth(meHandler.HandleMe))
	mux.HandleFunc("/api/me/nodes", auth(meHandler.HandleMyNodes))

	// Admin: User CRUD
	mux.HandleFunc("/api/users", admin(userHandler.ServeHTTP))
	mux.HandleFunc("/api/users/", admin(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access") {
			accessHandler.ServeHTTP(w, r)
		} else {
			userHandler.ServeHTTP(w, r)
		}
	}))

	// Admin: Node CRUD + ops
	mux.HandleFunc("/api/nodes", admin(nodeHandler.ServeHTTP))
	mux.HandleFunc("/api/nodes/reorder", admin(nodeHandler.Reorder))
	mux.HandleFunc("/api/nodes/", admin(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/generate") || strings.HasSuffix(path, "/push") ||
			strings.HasSuffix(path, "/raw-config") {
			configHandler.ServeHTTP(w, r)
		} else if strings.HasSuffix(path, "/version") || strings.HasSuffix(path, "/install") ||
			strings.HasSuffix(path, "/upgrade") || strings.HasSuffix(path, "/status") ||
			strings.HasSuffix(path, "/setup-ssh") {
			nodeOpsHandler.ServeHTTP(w, r)
		} else if strings.HasSuffix(path, "/cert") {
			validateHandler.HandleCertInstall(w, r)
		} else if strings.HasSuffix(path, "/cert-upload") {
			validateHandler.HandleCertUpload(w, r)
		} else if strings.HasSuffix(path, "/setup-assessment") {
			setupHandler.HandleSetupAssessment(w, r)
		} else if strings.HasSuffix(path, "/auto-setup") {
			setupHandler.HandleAutoSetup(w, r)
		} else if strings.HasSuffix(path, "/inbounds/reorder") {
			nodeHandler.ReorderInbounds(w, r)
		} else {
			nodeHandler.ServeHTTP(w, r)
		}
	}))
	mux.HandleFunc("/api/inbounds/", admin(nodeHandler.ServeHTTP))

	// Admin: validation, batch, stats
	mux.HandleFunc("/api/validate/dns", admin(validateHandler.HandleDNSCheck))
	mux.HandleFunc("/api/batch/push-all", admin(batchHandler.PushAll))
	mux.HandleFunc("/api/batch/template", admin(batchHandler.ApplyTemplate))
	mux.HandleFunc("/api/stats/users", admin(configHandler.HandleUserStats))
	mux.HandleFunc("/api/stats/nodes", admin(configHandler.HandleNodeStats))
	mux.HandleFunc("/api/stats/traffic", admin(configHandler.HandleTrafficHistory))

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
