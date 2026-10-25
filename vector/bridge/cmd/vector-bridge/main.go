// vector-bridge keeps an Anki Vector connected (credentials from wire-pod) and serves it to the
// Home Assistant integration over WebSocket.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/api"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/router"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wirepod"
)

var version = "dev"

func readSecret(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func main() {
	listen := flag.String("listen", ":3012", "API address")
	tokenFile := flag.String("token-file", "", "file with the API token (empty = no auth)")
	wp := flag.String("wirepod", "http://escapepod.local:8080", "wire-pod URL (empty = use -ip and -guid-file)")
	esn := flag.String("esn", "", "robot serial (empty = first robot in wire-pod)")
	name := flag.String("name", "Vector", "robot name")
	ip := flag.String("ip", "", "robot IP (overrides wire-pod)")
	guidFile := flag.String("guid-file", "", "file with the SDK GUID (overrides wire-pod)")
	level := flag.String("log-level", "info", "debug|info|warn|error")
	hookListen := flag.String("hook-listen", ":8790", "voice hook address for wire-pod (empty = off)")
	hookURL := flag.String("hook-url", "", "how wire-pod reaches the hook, e.g. http://192.168.1.10:8790")
	hookTokenFile := flag.String("hook-token-file", "", "file with the hook token")
	voiceRouter := flag.Bool("voice-router", true, "default state of the voice router")
	stateDir := flag.String("state-dir", "", "where to keep runtime state (voice router on/off)")
	flag.Parse()

	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(*level))
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	srv := api.New(version, readSecret(*tokenFile), log)
	cfg := robot.Config{ESN: *esn, Name: *name, IP: *ip, GUID: readSecret(*guidFile)}
	if *wp != "" {
		cfg.Wirepod = wirepod.New(*wp)
	}
	mgr := robot.NewManager(cfg, log.With("robot", *name), srv, nil)
	srv.AddRobot(mgr)

	var rt *router.Router
	if *hookListen != "" && cfg.Wirepod != nil && *hookURL != "" {
		stateFile := ""
		if *stateDir != "" {
			stateFile = filepath.Join(*stateDir, "voice_router")
		}
		rt = router.New(router.Config{
			WP: cfg.Wirepod, Token: readSecret(*hookTokenFile), HookURL: *hookURL, StateFile: stateFile,
			Log: log.With("component", "router"), Sink: srv,
			OnChange: func(bool) { srv.RouterChanged() },
		}, *voiceRouter)
		srv.Router = rt
	} else {
		log.Info("voice router off (needs -hook-listen, -hook-url and wire-pod)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go mgr.Run(ctx)
	if rt != nil {
		go rt.Run(ctx)
		hook := &http.Server{Addr: *hookListen, Handler: rt.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			_ = hook.Close()
		}()
		go func() {
			if err := hook.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("hook server", "err", err)
			}
		}()
	}

	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("vector-bridge started", "version", version, "listen", *listen, "wirepod", *wp)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}
