package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/emelvv/tvpoll/internal/api"
	"github.com/emelvv/tvpoll/internal/config"
	"github.com/emelvv/tvpoll/internal/ingest"
	"github.com/emelvv/tvpoll/internal/store"
	"github.com/emelvv/tvpoll/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	s, err := store.New(ctx, c.ControlURL, c.ShardURLs, int32(c.DBConns))
	cancel()
	if err != nil {
		return errors.New("database initialization failed; check connectivity and configuration")
	}
	defer s.Close()
	i := ingest.New(s, c.BatchSize, c.Workers, c.QueueSize, c.BatchWait, c.DBTimeout)
	defer i.Close()
	srv := &http.Server{Addr: c.Addr, Handler: api.New(s, i, c, web.Files(), &i.Metrics), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		slog.Info("Efir listening", "address", c.Addr, "shards", len(c.ShardURLs))
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-shutdownCtx.Done():
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
	// Stop new admissions, flush queued ballots, then let deferred pools close.
	i.Close()
	slog.Info("queues drained; shutdown complete")
	return nil
}
