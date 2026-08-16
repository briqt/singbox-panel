package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata" // panel time zone must resolve on hosts without a tz database

	"github.com/briqt/singbox-panel/config"
	"github.com/briqt/singbox-panel/db"
	"github.com/briqt/singbox-panel/handler"
	"github.com/briqt/singbox-panel/model"
)

// The admin SPA is compiled into the binary so a release stays a single file.
// Build it with `make web` (pnpm build) before `go build`.
//
//go:embed all:web/dist
var webDist embed.FS

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
	trafficStore := &model.TrafficStore{DB: database, Loc: cfg.Location}

	authHandler := &handler.AuthHandler{
		Users: userStore, AdminUser: cfg.AdminUser,
		AdminPass: cfg.AdminPass, JWTSecret: cfg.JWTSecret,
	}
	meHandler := &handler.MeHandler{Users: userStore, Nodes: nodeStore, Access: accessStore}
	subHandler := &handler.SubscriptionHandler{Users: userStore, Nodes: nodeStore, Access: accessStore}
	configHandler := &handler.ConfigHandler{Users: userStore, Nodes: nodeStore, Access: accessStore, Traffic: trafficStore, SSHKeyPath: cfg.SSHKeyPath}
	batchHandler := &handler.BatchHandler{Nodes: nodeStore, Config: configHandler}
	userHandler := &handler.UserHandler{Store: userStore, Access: accessStore, Sync: configHandler}
	accessHandler := &handler.AccessHandler{Access: accessStore, Nodes: nodeStore, Sync: configHandler}
	nodeHandler := &handler.NodeHandler{Store: nodeStore, Access: accessStore, Sync: configHandler}
	nodeOpsHandler := &handler.NodeOpsHandler{Nodes: nodeStore, Config: configHandler}
	setupHandler := &handler.SetupHandler{Nodes: nodeStore, Config: configHandler, Ops: nodeOpsHandler}
	validateHandler := &handler.ValidateHandler{Config: configHandler}
	statsHandler := &handler.StatsHandler{Users: userStore, Nodes: nodeStore, Traffic: trafficStore}

	trafficPoller := &handler.TrafficPoller{Nodes: nodeStore, Users: userStore, Traffic: trafficStore, Config: configHandler}
	trafficPoller.Start()
	defer trafficPoller.Close()

	admin := authHandler.AdminOnly
	auth := authHandler.JWTAuth

	mux := http.NewServeMux()

	mux.HandleFunc("/", spaHandler())

	// Liveness: the process is up. Deliberately checks nothing else, so a
	// supervisor never restarts the panel just because a dependency blipped.
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Readiness: the panel can actually serve. An unreachable database is a 503
	// so a probe can tell "starting up / degraded" apart from "dead".
	mux.HandleFunc("/api/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if err := database.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]any{"status": "unready", "database": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ready", "database": "ok"})
	})

	// Public
	mux.HandleFunc("/api/login", authHandler.HandleLogin)
	mux.HandleFunc("/api/register", authHandler.HandleRegister)

	// User self-service (any authenticated user)
	mux.HandleFunc("/api/me", auth(meHandler.HandleMe))
	mux.HandleFunc("/api/me/nodes", auth(meHandler.HandleMyNodes))
	mux.HandleFunc("/api/me/usage", auth(statsHandler.HandleMyUsage))

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

	// Admin: batch, stats
	mux.HandleFunc("/api/batch/push-all", admin(batchHandler.PushAll))
	mux.HandleFunc("/api/batch/template", admin(batchHandler.ApplyTemplate))
	mux.HandleFunc("/api/stats/meta", admin(statsHandler.HandleMeta))
	mux.HandleFunc("/api/stats/usage", admin(statsHandler.HandleUsage))
	mux.HandleFunc("/api/stats/users", admin(statsHandler.HandleUserStats))
	mux.HandleFunc("/api/stats/nodes", admin(statsHandler.HandleNodeStats))

	// Traffic report from node agents (auth via X-Node-Token)
	mux.HandleFunc("/api/node/report", configHandler.HandleTrafficReport)

	// Subscription (public, token in URL)
	mux.HandleFunc("/sub/", subHandler.ServeHTTP)

	addr := "127.0.0.1:" + cfg.Port
	log.Printf("singbox-panel listening on %s (timezone %s)", addr, cfg.Location)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// spaHandler serves the built assets and falls back to index.html so client
// routes survive a reload. Unmatched /api paths stay 404 instead of silently
// returning HTML.
func spaHandler() http.HandlerFunc {
	assets, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		log.Fatalf("embedded web assets: %v", err)
	}
	index, indexErr := fs.ReadFile(assets, "index.html")
	files := http.FileServer(http.FS(assets))

	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if indexErr != nil {
			http.Error(w, "frontend not built: run `make web` before building the binary", http.StatusServiceUnavailable)
			return
		}
		if path := strings.TrimPrefix(r.URL.Path, "/"); path != "" {
			if info, err := fs.Stat(assets, path); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	}
}
