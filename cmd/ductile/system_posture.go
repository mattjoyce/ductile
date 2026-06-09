package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
)

// checkBootPosture reports the LIVE gateway boot posture by probing /healthz —
// the authoritative signal (#130 anti-strand): a config-derived guess could lie
// about a daemon stuck pre-activation. It is informational (always OK) so it
// never changes overall health; the detail carries the posture.
func checkBootPosture(cfg *config.Config) systemStatusCheck {
	check := systemStatusCheck{Name: "boot_posture", OK: true}
	posture, ok := probeLivePosture(cfg)
	switch {
	case ok && posture == "management-only":
		check.Detail = "management-only (live) — vault-operable, public gateway not serving; mint an api token over the management socket, then reload to activate"
	case ok && posture != "":
		check.Detail = posture + " (live)"
	case ok:
		check.Detail = "gateway serving (live; posture field absent — older daemon)"
	default:
		check.Detail = "no live gateway reachable (daemon stopped, or not serving health)"
	}
	return check
}

// probeLivePosture asks a running daemon for its posture via /healthz. It tries
// the management socket first (only up in the bootstrap posture, so an
// unambiguous hit), then the gateway TCP listener. Best-effort with a short
// timeout: any error means "not reachable", never a hang.
func probeLivePosture(cfg *config.Config) (string, bool) {
	if p, ok := probeUnixHealthz(managementSocketPath(cfg)); ok {
		return p, true
	}
	if addr := dialableAddr(cfg.API.Listen); addr != "" {
		if p, ok := probeTCPHealthz(addr); ok {
			return p, true
		}
	}
	return "", false
}

func probeUnixHealthz(socket string) (string, bool) {
	client := &http.Client{
		Timeout: 500 * time.Millisecond,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
	return healthzPosture(client, "http://unix/healthz")
}

func probeTCPHealthz(addr string) (string, bool) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	return healthzPosture(client, "http://"+addr+"/healthz")
}

func healthzPosture(client *http.Client, url string) (string, bool) {
	resp, err := client.Get(url)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var body struct {
		Posture string `json:"posture"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false
	}
	return body.Posture, true
}

// dialableAddr turns a listen address into one a client can dial: a wildcard or
// empty host (0.0.0.0 / ::/ "") becomes 127.0.0.1. Returns "" if unusable.
func dialableAddr(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
