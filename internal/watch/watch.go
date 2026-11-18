// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package watch keeps one IMAP connection on an account's Inbox and tells a Sender when new
// mail arrives, or when the Outwright app's check message does.
package watch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/outwright/outwright-imap-push/internal/post"
)

// Marker starts the subject of the app's check message; the nonce follows it.
const Marker = "Outwright push check "

const (
	maxBatch   = 1000
	maxBackoff = 5 * time.Minute
	// stable is how long a session must last before reconnecting starts again from the
	// shortest wait: a server that drops the connection at once is not redialled every second.
	stable = time.Minute
)

// Account is one mailbox to watch.
type Account struct {
	Name     string
	Host     string
	Port     int
	TLS      string // "tls" or "starttls"; empty only for a server on loopback
	User     string
	Password string
	// Poll is how often a server without IDLE is asked.
	Poll time.Duration
	// RootCAs, when set, are the only certificate authorities trusted for the server (an owner's
	// own CA); nil trusts the system's.
	RootCAs *x509.CertPool
}

// Sender is where wake-ups go.
type Sender interface {
	NewMail(ctx context.Context) error
	Check(ctx context.Context, nonce string) error
}

var (
	firstBackoff = time.Second
	// settle is how long a change waits for more before the scan, so a burst posts once.
	settle = time.Second
	// idles says whether to IDLE; tests turn it off, since the fake server always offers it.
	idles = func(c *imapclient.Client) bool { return c.Caps().Has(imap.CapIdle) }
	// synced is called after every scan (tests wait on it).
	synced = func(string) {}
	// idleRefresh is how long one IDLE lasts before it is ended and the Inbox scanned again:
	// RFC 2177 asks for a new IDLE within 29 minutes, and mail that landed between a scan and
	// the IDLE after it, which a server need not tell of, is found by then.
	idleRefresh = 25 * time.Minute
	// beforeIdle is called before each IDLE is asked for, idling once the server is in it (tests).
	beforeIdle = func(string) {}
	idling     = func(string) {}
)

// Run watches the account until ctx ends, reconnecting with backoff. It returns early only
// for a configuration it refuses or when the push address is gone (post.ErrGone).
func Run(ctx context.Context, a Account, st *State, s Sender) error {
	if err := a.check(); err != nil {
		return err
	}
	log := slog.Default().With("account", a.Name)
	var wait time.Duration
	for {
		start := time.Now()
		err := session(ctx, a, st, s)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, post.ErrGone) {
			return err
		}
		wait = nextWait(wait, time.Since(start))
		log.Warn("connection ended, reconnecting", "error", err, "in", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// nextWait is the wait before reconnecting: the shortest after a session that lasted, else
// twice the last one, up to maxBackoff.
func nextWait(last, lasted time.Duration) time.Duration {
	if last == 0 || lasted >= stable {
		return firstBackoff
	}
	return min(last*2, maxBackoff)
}

func (a Account) check() error {
	switch a.TLS {
	case "tls", "starttls":
		return nil
	case "":
		if post.IsLoopback(a.Host) {
			return nil
		}
		return fmt.Errorf("account %q: tls must be \"tls\" or \"starttls\"", a.Name)
	default:
		return fmt.Errorf("account %q: unknown tls %q", a.Name, a.TLS)
	}
}

func session(ctx context.Context, a Account, st *State, s Sender) error {
	changed := make(chan struct{}, 1)
	opts := &imapclient.Options{TLSConfig: &tls.Config{RootCAs: a.RootCAs}, UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				select {
				case changed <- struct{}{}:
				default:
				}
			}
		},
	}}
	addr := net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
	var c *imapclient.Client
	var err error
	switch a.TLS {
	case "tls":
		c, err = imapclient.DialTLS(addr, opts)
	case "starttls":
		c, err = imapclient.DialStartTLS(addr, opts)
	default:
		c, err = imapclient.DialInsecure(addr, opts)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	if err := c.Login(a.User, a.Password).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	sel, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return fmt.Errorf("select: %w", err)
	}
	if err := scan(ctx, c, sel, st, s, a.Name); err != nil {
		return err
	}
	idle := idles(c)
	for {
		if idle {
			beforeIdle(a.Name)
			cmd, err := c.Idle()
			if err != nil {
				return fmt.Errorf("idle: %w", err)
			}
			idling(a.Name)
			refresh := time.NewTimer(idleRefresh)
			select {
			case <-ctx.Done():
				refresh.Stop()
				cmd.Close()
				return nil
			case <-c.Closed():
				refresh.Stop()
				return errors.New("server closed the connection")
			case <-changed:
				refresh.Stop()
				select {
				case <-ctx.Done():
					cmd.Close()
					return nil
				case <-time.After(settle):
				}
			case <-refresh.C:
			}
			if err := cmd.Close(); err != nil {
				return fmt.Errorf("idle: %w", err)
			}
		} else {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(a.Poll):
			}
			if err := c.Noop().Wait(); err != nil {
				return fmt.Errorf("noop: %w", err)
			}
		}
		if err := scan(ctx, c, sel, st, s, a.Name); err != nil {
			return err
		}
	}
}

// scan looks at every message above the last UID seen. A first start or a changed
// UIDVALIDITY takes everything there as seen and announces nothing.
func scan(ctx context.Context, c *imapclient.Client, sel *imap.SelectData, st *State, s Sender, name string) error {
	defer synced(name)
	validity, last := st.get()
	if validity != sel.UIDValidity {
		top, err := highest(c, sel)
		if err != nil {
			return err
		}
		st.set(sel.UIDValidity, top)
		return st.Save()
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{
		UID: []imap.UIDSet{{imap.UIDRange{Start: imap.UID(last + 1), Stop: 0}}},
	}, nil).Wait()
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	var uids []imap.UID
	for _, u := range data.AllUIDs() {
		if uint32(u) > last {
			uids = append(uids, u)
		}
	}
	if len(uids) == 0 {
		return nil
	}
	slices.Sort(uids)
	uids = uids[:min(len(uids), maxBatch)]
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, Flags: true, Envelope: true}).Collect()
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	newMail := false
	top := last
	for _, m := range msgs {
		top = max(top, uint32(m.UID))
		subject := ""
		if m.Envelope != nil {
			subject = m.Envelope.Subject
		}
		if nonce, ok := strings.CutPrefix(subject, Marker); ok {
			if validNonce(nonce) {
				if err := s.Check(ctx, nonce); err != nil {
					// Not seen yet: the next scan, after reconnecting, posts it again.
					return fmt.Errorf("check not posted: %w", err)
				}
			}
			continue
		}
		if !slices.Contains(m.Flags, imap.FlagSeen) {
			newMail = true
		}
	}
	if newMail {
		if err := s.NewMail(ctx); err != nil {
			// Not seen yet: the next scan, after reconnecting, posts it again.
			return fmt.Errorf("new mail not posted: %w", err)
		}
	}
	st.set(validity, top)
	return st.Save()
}

// highest is the highest UID in the mailbox now.
func highest(c *imapclient.Client, sel *imap.SelectData) (uint32, error) {
	if sel.UIDNext > 0 {
		return uint32(sel.UIDNext) - 1, nil
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("search: %w", err)
	}
	var top uint32
	for _, u := range data.AllUIDs() {
		top = max(top, uint32(u))
	}
	return top, nil
}

// validNonce: base64url without padding, 16 to 64 characters, as the relay takes it.
func validNonce(n string) bool {
	if len(n) < 16 || len(n) > 64 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(n)
	return err == nil
}
