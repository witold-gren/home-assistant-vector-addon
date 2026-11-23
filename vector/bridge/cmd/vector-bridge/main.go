// vector-bridge keeps an Anki Vector connected (credentials from wire-pod) and serves it to the
// Home Assistant integration over WebSocket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/ai"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/api"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/ha"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/router"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wirepod"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wyoming"
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
	studioTokenFile := flag.String("studio-token-file", "", "file with the Robots Code Studio token (empty = no studio)")
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
	rtspPort := flag.Int("rtsp-port", 0, "RTSP port of the add-on's go2rtc live stream (0 = no stream)")
	streamAudio := flag.Bool("stream-audio", true, "the live stream carries the robot's microphone")
	studioParentTokenFile := flag.String("studio-parent-token-file", "", "file with the studio's parent token (empty = no photos/faces from the studio)")
	piperURL := flag.String("piper-url", "", "Wyoming Piper server, tcp://host:10200 (empty = wire-pod speaks)")
	piperVoice := flag.String("piper-voice", "", "default Piper voice (empty = the server's default)")
	aiURL := flag.String("ai-url", "", "Ollama server for ask_ai, http://host:11434 (empty = off)")
	aiModel := flag.String("ai-model", "", "Ollama model for ask_ai")
	haURL := flag.String("ha-url", "", "Home Assistant API for the weather, e.g. http://supervisor/core (token: SUPERVISOR_TOKEN)")
	weatherEntity := flag.String("weather-entity", "", "weather entity (empty = the first weather.*)")
	flag.Parse()

	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(*level))
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	srv := api.New(version, readSecret(*tokenFile), log)
	srv.StudioToken = readSecret(*studioTokenFile)
	if srv.StudioToken != "" && srv.StudioToken == srv.Token {
		log.Warn("studio token equals the API token: the studio gets full access")
	}
	srv.StudioParentToken = readSecret(*studioParentTokenFile)
	if srv.StudioParentToken != "" && (srv.StudioParentToken == srv.StudioToken || srv.StudioParentToken == srv.Token) {
		log.Warn("studio parent token equals another token: parent commands off")
		srv.StudioParentToken = ""
	}
	if *rtspPort > 0 {
		srv.Stream = &api.StreamInfo{RTSPPort: *rtspPort, Path: "vector", Username: "vector", Audio: *streamAudio}
		// the internal viewer logs in like Home Assistant does (user vector, the API token)
		srv.Warm = api.NewWarmer(fmt.Sprintf("rtsp://vector:%s@127.0.0.1:%d/vector",
			url.QueryEscape(readSecret(*tokenFile)), *rtspPort), log.With("component", "stream"))
		defer srv.Warm.Stop()
	}
	cfg := robot.Config{ESN: *esn, Name: *name, IP: *ip, GUID: readSecret(*guidFile)}
	if *wp != "" {
		cfg.Wirepod = wirepod.New(*wp)
	}
	if cfg.IP != "" && cfg.GUID != "" {
		log.Info("robot address and GUID set by hand: wire-pod is not asked for them")
	}
	features := []string{}
	if *piperURL != "" {
		if tts, err := wyoming.New(*piperURL, *piperVoice); err != nil {
			log.Error("piper_url ignored", "err", err)
		} else {
			cfg.TTS = tts
			features = append(features, "voices")
			log.Info("own voices: Wyoming Piper", "server", tts.Addr, "voice", tts.Voice)
		}
	}
	if asker, err := ai.New(*aiURL, *aiModel); err != nil {
		log.Error("ask_ai off", "err", err)
	} else if asker != nil {
		cfg.AI = asker
		features = append(features, "ai")
		log.Info("ask_ai: local LLM", "model", asker.Model)
	}
	if w := ha.New(*haURL, os.Getenv("SUPERVISOR_TOKEN"), *weatherEntity); w != nil {
		cfg.Weather = w
		features = append(features, "weather")
	}
	// always there: Vector's behaviors, volume, mirror mode (listening: with the voice router below)
	features = append(features, "behavior", "volume", "mirror")
	if srv.StudioParentToken != "" {
		features = append(features, "parent")
	}
	srv.ExtraFeatures = features
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
			Log: log.With("component", "router"), Sink: srv, Programs: mgr,
			OnChange: func(bool) { srv.RouterChanged() },
		}, *voiceRouter)
		srv.Router = rt
		mgr.SetClaimHook(rt.SetClaimed)
		srv.ExtraFeatures = append(srv.ExtraFeatures, "listen")
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
