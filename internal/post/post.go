// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package post sends wake-ups to an Outwright push address: an empty body for new mail,
// `check:<nonce>` for the app's check. Nothing of a message is ever sent.
package post

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrGone means the address no longer exists: push was switched off in the app.
var ErrGone = errors.New("push address gone")

const (
	tries        = 3
	defaultRetry = 30 * time.Second
)

// Client posts to one push address.
type Client struct {
	URL  string
	HTTP *http.Client
	// retryWait is how long a 429 waits before the next try; 0 is 30 seconds.
	retryWait time.Duration
}

// NewMail tells the address new mail arrived.
func (c Client) NewMail(ctx context.Context) error { return c.post(ctx, "") }

// Check answers the app's check with its nonce.
func (c Client) Check(ctx context.Context, nonce string) error { return c.post(ctx, "check:"+nonce) }

func (c Client) post(ctx context.Context, body string) error {
	if err := Allowed(c.URL); err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	wait := c.retryWait
	if wait == 0 {
		wait = defaultRetry
	}
	for try := 1; ; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "text/plain")
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusNotFound:
			return ErrGone
		case resp.StatusCode == http.StatusTooManyRequests && try < tries:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		default:
			return fmt.Errorf("push address answered %d", resp.StatusCode)
		}
	}
}

// Allowed says whether an address may be posted to: https, or http to loopback only.
func Allowed(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return errors.New("push address is not a URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if IsLoopback(u.Hostname()) {
			return nil
		}
	}
	return errors.New("push address must be https")
}

// IsLoopback says whether host is this machine.
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
