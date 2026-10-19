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
	// telesrv-update publish [flags] <包文件>：发布一个桌面包；其余调用方式保持原样（serve / -check）。
	if len(os.Args) > 1 && os.Args[1] == "publish" {
		return runPublish(os.Args[2:])
	}

	listenDefault := envOr("TELESRV_UPDATE_LISTEN", "127.0.0.1:2402")

	listenAddr := flag.String("listen", listenDefault, "HTTP listen address")
	manifestPath, filesDir, rootKeyPath := catalogFlags(flag.CommandLine)
	check := flag.Bool("check", false, "validate the catalog and exit")
	flag.Parse()
	// 服务模式不接受位置参数：把参数写在 publish 前面（telesrv-update -manifest m publish x）
	// 会落到这里，不能悄悄启动服务。
	if flag.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q; use `telesrv-update publish [flags] <package>` to publish", flag.Args())
	}

	// 未配置根公钥时仍可只提供移动端条目；一旦清单有启用的桌面条目，加载会失败。
	var opts updatecdn.CatalogOptions
	if *rootKeyPath != "" {
		rootKey, err := updatecdn.LoadRootPublicKey(*rootKeyPath)
		if err != nil {
			return err
		}
		opts.RootPublicKey = rootKey
	}
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("initialize logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	// 重新加载失败时继续提供上一份正常快照，只记录错误。
	store, err := updatecdn.NewStore(*manifestPath, *filesDir, opts,
		updatecdn.WithReloadErrorHandler(func(err error) {
			logger.Error("update manifest reload failed; serving the previous catalog",
				zap.String("manifest", *manifestPath), zap.Error(err))
		}))
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

// catalogFlags 注册服务模式与 publish 共用的清单、文件目录与根公钥参数，默认值与环境变量一致。
func catalogFlags(fs *flag.FlagSet) (manifestPath, filesDir, rootKeyPath *string) {
	manifestPath = fs.String("manifest", envOr("TELESRV_UPDATE_MANIFEST", "data/updates/manifest.json"), "release manifest path")
	filesDir = fs.String("files", envOr("TELESRV_UPDATE_FILES_DIR", "data/updates/files"), "desktop update package directory")
	rootKeyPath = fs.String("root-public-key", os.Getenv("TELESRV_UPDATE_ROOT_PUBLIC_KEY"),
		"update root public key PEM file (Ed25519); required when the manifest has enabled desktop entries")
	return manifestPath, filesDir, rootKeyPath
}

// runPublish 验签一个 v2 桌面包，把它拷进文件目录并原子更新清单，输出发布的条目。
func runPublish(args []string) error {
	fs := flag.NewFlagSet("telesrv-update publish", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: telesrv-update publish [flags] <package>")
		fs.PrintDefaults()
	}
	manifestPath, filesDir, rootKeyPath := catalogFlags(fs)
	dryRun := fs.Bool("dry-run", false, "verify the package and the resulting manifest without writing any file")
	allowDowngrade := fs.Bool("allow-downgrade", false, "allow a build not newer than the published one (roll back a bad release)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("publish takes exactly one package path (flags go before it), got %d arguments", fs.NArg())
	}
	if *rootKeyPath == "" {
		return errors.New("publish requires the update root public key (-root-public-key or TELESRV_UPDATE_ROOT_PUBLIC_KEY)")
	}
	rootKey, err := updatecdn.LoadRootPublicKey(*rootKeyPath)
	if err != nil {
		return err
	}
	result, err := updatecdn.PublishDesktopPackage(*manifestPath, *filesDir, fs.Arg(0), updatecdn.PublishOptions{
		CatalogOptions: updatecdn.CatalogOptions{RootPublicKey: rootKey},
		DryRun:         *dryRun,
		AllowDowngrade: *allowDowngrade,
	})
	if err != nil {
		return fmt.Errorf("publish %s: %w", fs.Arg(0), err)
	}

	var status string
	switch {
	case result.Unchanged:
		status = "already published"
	case !result.ManifestUpdated && *dryRun:
		status = "dry run: would restore the missing package file of"
	case !result.ManifestUpdated:
		status = "restored the missing package file of"
	case *dryRun:
		status = "dry run: would publish"
	default:
		status = "published"
	}
	release := result.Release
	fmt.Printf("%s desktop.%s.%s\n", status, result.Platform, result.Channel)
	fmt.Printf("  build:   %d (package version %d.%d, created %s)\n", release.Build,
		result.Package.Base(), result.Package.Counter(), result.Package.Created.UTC().Format(time.RFC3339))
	fmt.Printf("  file:    %s\n", release.File)
	fmt.Printf("  size:    %d\n", release.Size)
	fmt.Printf("  sha256:  %s\n", release.SHA256)
	if previous := result.Previous; previous != nil && result.ManifestUpdated {
		state := ""
		if previous.Disabled {
			state = ", disabled"
		}
		fmt.Printf("  replaces: build %d (%s%s)\n", previous.Build, previous.File, state)
	}
	fmt.Printf("  manifest: %s\n", *manifestPath)
	return nil
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
