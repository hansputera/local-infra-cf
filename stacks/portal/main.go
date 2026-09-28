package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"portal/internal/config"
	"portal/internal/engine"
	"portal/internal/registry"
	"portal/internal/web"
)

func main() {
	cfg := config.FromEnv()
	if cfg.CFToken == "" {
		log.Fatal("CF_API_TOKEN kosong (isi lewat env_file secrets/cf-api-token.env)")
	}

	regPath := filepath.Join(cfg.InfraRoot, "data", "portal", "registry.json")
	reg, err := registry.Open(regPath)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}

	nodesPath := filepath.Join(cfg.InfraRoot, "data", "portal", "nodes.json")
	ns, err := registry.OpenNodes(nodesPath)
	if err != nil {
		log.Fatalf("registry node: %v", err)
	}

	eng := engine.New(cfg, reg, ns)
	app, err := web.New(cfg, eng)
	if err != nil {
		log.Fatalf("web: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if strings.EqualFold(os.Getenv("PORTAL_AUTORECONCILE"), "true") {
			time.Sleep(3 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			for _, e := range eng.ReconcileAll(ctx) {
				log.Printf("auto-reconcile: %v", e)
			}
			log.Printf("auto-reconcile selesai (%d service)", len(reg.List()))
		}
	}()

	go func() {
		log.Printf("portal mendengarkan di %s (root %s, domain %s)", cfg.Listen, cfg.InfraRoot, cfg.PublicDomain)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("menutup portal")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
