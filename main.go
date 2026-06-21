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

	userHandler := &handler.UserHandler{Store: userStore}
	nodeHandler := &handler.NodeHandler{Store: nodeStore}
	subHandler := &handler.SubscriptionHandler{Users: userStore, Nodes: nodeStore, Access: accessStore}
	configHandler := &handler.ConfigHandler{Users: userStore, Nodes: nodeStore, Access: accessStore, SSHKeyPath: cfg.SSHKeyPath}
	batchHandler := &handler.BatchHandler{Nodes: nodeStore, Config: configHandler}
	authHandler := &handler.AuthHandler{Users: userStore, AdminToken: cfg.AdminToken}
	accessHandler := &handler.AccessHandler{Access: accessStore, Nodes: nodeStore}
	nodeOpsHandler := &handler.NodeOpsHandler{Nodes: nodeStore, Config: configHandler}
	setupHandler := &handler.SetupHandler{Nodes: nodeStore, Config: configHandler, Ops: nodeOpsHandler}

	mux := http.NewServeMux()

	mux.HandleFunc("/admin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(adminHTML)
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Public: registration
	mux.HandleFunc("/api/register", authHandler.HandleRegister)

	// User CRUD + batch ops (admin)
	mux.HandleFunc("/api/users", adminAuth(cfg.AdminToken, userHandler.ServeHTTP))
	mux.HandleFunc("/api/users/", adminAuth(cfg.AdminToken, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access") {
			accessHandler.ServeHTTP(w, r)
		} else {
			userHandler.ServeHTTP(w, r)
		}
	}))

	// Node CRUD + config ops (admin)
	validateHandler := &handler.ValidateHandler{Config: configHandler}
	mux.HandleFunc("/api/nodes", adminAuth(cfg.AdminToken, nodeHandler.ServeHTTP))
	mux.HandleFunc("/api/nodes/", adminAuth(cfg.AdminToken, func(w http.ResponseWriter, r *http.Request) {
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
		} else if strings.HasSuffix(path, "/auto-setup") {
			setupHandler.HandleAutoSetup(w, r)
		} else {
			nodeHandler.ServeHTTP(w, r)
		}
	}))
	mux.HandleFunc("/api/inbounds/", adminAuth(cfg.AdminToken, nodeHandler.ServeHTTP))

	// DNS validation (admin)
	mux.HandleFunc("/api/validate/dns", adminAuth(cfg.AdminToken, validateHandler.HandleDNSCheck))

	// Batch operations (admin)
	mux.HandleFunc("/api/batch/push-all", adminAuth(cfg.AdminToken, batchHandler.PushAll))
	mux.HandleFunc("/api/batch/template", adminAuth(cfg.AdminToken, batchHandler.ApplyTemplate))

	// Stats (admin)
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
