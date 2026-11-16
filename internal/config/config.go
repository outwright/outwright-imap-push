// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package config reads the worker's TOML file.
package config

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/outwright/outwright-imap-push/internal/post"
	"github.com/outwright/outwright-imap-push/internal/watch"
)

// DefaultPoll is how often a server without IDLE is asked when the file does not say.
const DefaultPoll = 5 * time.Minute

// Account is one configured mailbox and where its wake-ups go.
type Account struct {
	Watch       watch.Account
	PushAddress string
}

type file struct {
	Account []struct {
		Name         string `toml:"name"`
		Host         string `toml:"host"`
		Port         int    `toml:"port"`
		TLS          string `toml:"tls"`
		User         string `toml:"user"`
		Password     string `toml:"password"`
		PasswordFile string `toml:"password_file"`
		PushAddress  string `toml:"push_address"`
		CAFile       string `toml:"ca_file"`
		Poll         string `toml:"poll"`
	} `toml:"account"`
}

// Load reads and checks the file at path. Errors name the account, never a password.
func Load(path string) ([]Account, error) {
	var f file
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, fmt.Errorf("config: unknown setting %q", un[0].String())
	}
	if len(f.Account) == 0 {
		return nil, errors.New("config: no [[account]]")
	}
	seen := map[string]bool{}
	var out []Account
	for i, a := range f.Account {
		name := a.Name
		if name == "" {
			return nil, fmt.Errorf("config: account %d has no name", i+1)
		}
		bad := func(what string) error { return fmt.Errorf("config: account %q: %s", name, what) }
		if seen[name] {
			return nil, bad("name used twice")
		}
		seen[name] = true
		if a.Host == "" || a.User == "" {
			return nil, bad("host and user are needed")
		}
		if a.Port < 1 || a.Port > 65535 {
			return nil, bad("port must be 1 to 65535")
		}
		if a.TLS != "tls" && a.TLS != "starttls" && !(a.TLS == "" && post.IsLoopback(a.Host)) {
			return nil, bad(`tls must be "tls" or "starttls"`)
		}
		password := a.Password
		switch {
		case a.Password != "" && a.PasswordFile != "":
			return nil, bad("give password or password_file, not both")
		case a.PasswordFile != "":
			b, err := os.ReadFile(a.PasswordFile)
			if err != nil {
				return nil, bad("password_file cannot be read")
			}
			password = strings.TrimRight(string(b), "\r\n")
		}
		if password == "" {
			return nil, bad("password or password_file is needed")
		}
		if err := post.Allowed(a.PushAddress); err != nil {
			return nil, bad(err.Error())
		}
		var roots *x509.CertPool
		if a.CAFile != "" {
			b, err := os.ReadFile(a.CAFile)
			if err != nil {
				return nil, bad("ca_file cannot be read")
			}
			roots = x509.NewCertPool()
			if !roots.AppendCertsFromPEM(b) {
				return nil, bad("ca_file has no PEM certificate")
			}
		}
		poll := DefaultPoll
		if a.Poll != "" {
			if poll, err = time.ParseDuration(a.Poll); err != nil || poll < 10*time.Second {
				return nil, bad("poll must be a duration of at least 10s")
			}
		}
		out = append(out, Account{
			Watch: watch.Account{
				Name: name, Host: a.Host, Port: a.Port, TLS: a.TLS, User: a.User, Password: password, Poll: poll,
				RootCAs: roots,
			},
			PushAddress: a.PushAddress,
		})
	}
	return out, nil
}
