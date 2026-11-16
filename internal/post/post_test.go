// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package post

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type seen struct {
	mu     sync.Mutex
	bodies []string
	types  []string
}

func server(t *testing.T, status int, s *seen) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(b))
		s.types = append(s.types, r.Header.Get("Content-Type"))
		s.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPostBodies(t *testing.T) {
	var s seen
	srv := server(t, http.StatusAccepted, &s)
	c := Client{URL: srv.URL + "/v1/channels/abc/plain"}
	if err := c.NewMail(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Check(context.Background(), "AAAAAAAAAAAAAAAAAAAAAA"); err != nil {
		t.Fatal(err)
	}
	if len(s.bodies) != 2 || s.bodies[0] != "" || s.bodies[1] != "check:AAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("bodies %q", s.bodies)
	}
	for _, ct := range s.types {
		if ct != "text/plain" {
			t.Fatalf("content type %q", ct)
		}
	}
}

func TestPostRefusesHTTP(t *testing.T) {
	c := Client{URL: "http://push.example.org/v1/channels/abc/plain"}
	if err := c.NewMail(context.Background()); err == nil {
		t.Fatal("posted over plain http off loopback")
	}
}

func TestPost404IsGone(t *testing.T) {
	var s seen
	srv := server(t, http.StatusNotFound, &s)
	c := Client{URL: srv.URL + "/v1/channels/abc/plain"}
	if err := c.NewMail(context.Background()); !errors.Is(err, ErrGone) {
		t.Fatalf("want ErrGone, got %v", err)
	}
}

func TestPost429Retries(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	c := Client{URL: srv.URL, retryWait: 1}
	if err := c.NewMail(context.Background()); err != nil || calls != 2 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}
