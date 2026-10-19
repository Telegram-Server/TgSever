// Command telesrv-update serves native Telegram client update metadata and
// immutable, range-enabled desktop update packages.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/updatecdn"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "telesrv-update:", err)
		os.Exit(1)
	}
}

func run() error {
	listenDefault := envOr("TELESRV_UPDATE_LISTEN", "127.0.0.1:2402")
	manifestDefault := envOr("TELESRV_UPDATE_MANIFEST", "data/updates/manifest.json")
	filesDefault := envOr("TELESRV_UPDATE_FILES_DIR", "data/updates/files")

	listenAddr := flag.String("listen", listenDefault, "HTTP listen address")
	manifestPath := flag.String("manifest", manifestDefault, "release manifest path")
	filesDir := flag.String("files", filesDefault, "desktop update package directory")
	rootKeyPath := flag.String("root-public-key", os.Getenv("TELESRV_UPDATE_ROOT_PUBLIC_KEY"),
		"update root public key PEM file (Ed25519); required when the manifest has enabled desktop entries")
	check := flag.Bool("check", false, "validate the catalog and exit")
	flag.Parse()

	// 未配置根公钥时仍可只提供移动端条目；一旦清单有启用的桌面条目，加载会失败。
	var opts updatecdn.CatalogOptions
	if *rootKeyPath != "" {
		rootKey, err := updatecdn.LoadRootPublicKey(*rootKeyPath)
		if err != nil {
			return err
		}
		opts.RootPublicKey = rootKey
	}
	store, err := updatecdn.NewStore(*manifestPath, *filesDir, opts)
	if err != nil {
		return fmt.Errorf("load update catalog: %w", err)
	}
	if *check {
		fmt.Println("update catalog is valid")
		return nil
	}
	handler, err := updatecdn.NewHandler(store)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *listenAddr, err)
	}

	logger, err := zap.NewProduction()
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("initialize logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	logger.Info("update service started",
		zap.String("listen", listener.Addr().String()),
		zap.String("manifest", *manifestPath),
		zap.String("files", *filesDir),
		zap.String("root_public_key", *rootKeyPath))

	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-stopCtx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	logger.Info("update service stopped")
	return nil
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
