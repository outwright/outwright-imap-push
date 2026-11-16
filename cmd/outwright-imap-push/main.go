// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command outwright-imap-push watches IMAP Inboxes and wakes the Outwright app through its
// push address when new mail arrives. An example: see README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/outwright/outwright-imap-push/internal/config"
	"github.com/outwright/outwright-imap-push/internal/post"
	"github.com/outwright/outwright-imap-push/internal/watch"
)

func main() {
	cfgPath := flag.String("config", env("OUTWRIGHT_IMAP_PUSH_CONFIG", "/config/config.toml"), "the config file")
	stateDir := flag.String("state", env("OUTWRIGHT_IMAP_PUSH_STATE", "/state"), "where what was seen is kept")
	flag.Parse()

	accounts, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("cannot start", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	for _, a := range accounts {
		st, err := watch.LoadState(*stateDir, a.Watch.Name)
		if err != nil {
			slog.Error("cannot read state", "account", a.Watch.Name, "error", err)
			os.Exit(2)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slog.Info("watching", "account", a.Watch.Name, "host", a.Watch.Host)
			err := watch.Run(ctx, a.Watch, st, post.Client{URL: a.PushAddress})
			switch {
			case errors.Is(err, post.ErrGone):
				slog.Warn("push address is gone (push switched off in the app); this account stops", "account", a.Watch.Name)
			case err != nil:
				slog.Error("stopped", "account", a.Watch.Name, "error", err)
			}
		}()
	}
	wg.Wait()
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
