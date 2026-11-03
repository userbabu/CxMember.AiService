package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cx-ai-relay/internal/relay"
)

func main() {
	cfg := relay.LoadConfig()
	store, err := relay.OpenStore(cfg.DatabaseDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go relay.RunUsageCleanup(ctx, store)
	relayServer := relay.NewServer(cfg, store)
	go relayServer.RunGenerations(ctx)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: relayServer.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("CX AI relay listening on %s", cfg.HTTPAddr)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown failed: %v", err)
		}
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Print(err)
		os.Exit(1)
	}
}
