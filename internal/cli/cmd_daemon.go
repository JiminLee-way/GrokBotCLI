package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gb/internal/config"
	"gb/internal/daemon"
)

func socketPath(p *config.Profile) string { return filepath.Join(p.Dir, "gbd.sock") }

func cmdDaemon(ctx context.Context, e *env) error {
	sub := "status"
	if len(e.a.pos) > 0 {
		sub = e.a.pos[0]
	}
	p, err := config.Open(e.profile)
	if err != nil {
		return err
	}
	switch sub {
	case "run":
		var cfg daemon.Config
		if err := p.ReadJSON("gbd.json", &cfg); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("gbd.json: %w", err)
		}
		if v := e.a.get("listen", ""); v != "" {
			cfg.Listen = v
		}
		if v := os.Getenv("GBD_TOKEN"); v != "" {
			cfg.Token = v
		}
		lg := log.New(os.Stderr, "gbd ", log.LstdFlags)
		if err := cfg.ACL.Validate(); err != nil {
			return fmt.Errorf("gbd.json: %w", err)
		}
		if cfg.ACL == nil || len(cfg.ACL.Rules) == 0 {
			lg.Printf("warning: no \"acl\" in gbd.json — every caller may use every bot")
		}
		x, err := e.API()
		if err != nil {
			return err
		}
		hub := daemon.NewHub(x, lg)
		go func() {
			if err := hub.Run(ctx); err != nil && ctx.Err() == nil {
				lg.Printf("event loop stopped: %v", err)
			}
		}()
		daemon.RunHooks(ctx, hub, cfg.Hooks, lg)
		lg.Printf("gbd %s profile=%s hooks=%d", Version, p.Name, len(cfg.Hooks))
		return (&daemon.Server{Hub: hub, Cfg: cfg, Log: lg}).Serve(ctx, socketPath(p))

	case "status":
		c := unixClient(socketPath(p))
		res, err := c.Get("http://gbd/v1/health")
		if err != nil {
			e.emit(map[string]any{"running": false, "socket": socketPath(p)}, func(w io.Writer) {
				fmt.Fprintf(w, "gbd is not running (socket %s)\n", socketPath(p))
			})
			return exitError{3, errors.New("gbd not running")}
		}
		defer res.Body.Close()
		var h map[string]any
		_ = json.NewDecoder(res.Body).Decode(&h)
		h["running"], h["socket"] = true, socketPath(p)
		e.emit(h, func(w io.Writer) {
			fmt.Fprintf(w, "gbd running: events connected=%v, %v bots/groups, socket %s\n", h["eventsConnected"], h["agents"], socketPath(p))
		})
		return nil

	case "install":
		return daemonInstall(e, p)
	}
	return usage("gb daemon run|status|install [--system]")
}

func unixClient(sock string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
}

func daemonInstall(e *env, p *config.Profile) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd units are Linux-only; on %s run `gb daemon run` (e.g. under tmux or launchd)", runtime.GOOS)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	envLines := ""
	for _, k := range []string{"GB_HOME", "XDG_CONFIG_HOME"} {
		if v := os.Getenv(k); v != "" {
			envLines += fmt.Sprintf("Environment=%s=%s\n", k, v)
		}
	}
	system := e.a.bool("system")
	user := os.Getenv("USER")
	unitName := "gbd.service"
	if p.Name != "default" {
		unitName = "gbd-" + p.Name + ".service"
	}
	unit := fmt.Sprintf(`[Unit]
Description=gbd — Grok Bot gateway daemon (profile %[1]s)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%[2]s daemon run --profile %[1]s
%[3]sRestart=always
RestartSec=5
NoNewPrivileges=true
`, p.Name, exe, envLines)
	var path, next string
	if system {
		home, _ := os.UserHomeDir()
		unit += fmt.Sprintf("User=%s\nEnvironment=HOME=%s\n\n[Install]\nWantedBy=multi-user.target\n", user, home)
		path = filepath.Join("/etc/systemd/system", unitName)
		next = "sudo systemctl daemon-reload && sudo systemctl enable --now " + unitName
	} else {
		unit += "\n[Install]\nWantedBy=default.target\n"
		home, _ := os.UserHomeDir()
		dir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path = filepath.Join(dir, unitName)
		next = "systemctl --user daemon-reload && systemctl --user enable --now " + unitName +
			"\n  loginctl enable-linger " + user + "   # keep it running after you log out (headless servers)"
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		if system && errors.Is(err, os.ErrPermission) {
			fmt.Fprintf(e.out, "Write this to %s as root:\n\n%s\n", path, unit)
			fmt.Fprintf(e.out, "Then:\n  %s\n", next)
			return nil
		}
		return err
	}
	fmt.Fprintf(e.out, "Wrote %s\nNext:\n  %s\nLogs: journalctl %s-u %s -f\n", path, next, map[bool]string{false: "--user ", true: ""}[system], strings.TrimSuffix(unitName, ".service"))
	return nil
}
