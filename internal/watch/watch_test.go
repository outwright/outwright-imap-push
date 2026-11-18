// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package watch

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

type recorder struct {
	mu      sync.Mutex
	newMail int
	checks  []string
	// failNext makes that many NewMail calls fail, as a relay that cannot be reached.
	failNext int
	tries    int
}

func (r *recorder) NewMail(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tries++
	if r.failNext > 0 {
		r.failNext--
		return errors.New("relay unreachable")
	}
	r.newMail++
	return nil
}

func (r *recorder) Check(_ context.Context, nonce string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, nonce)
	return nil
}

func (r *recorder) counts() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.newMail, append([]string(nil), r.checks...)
}

type fake struct {
	host string
	port int
}

func startServer(t *testing.T, caps imap.CapSet) fake {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("me@example.org", "secret")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         caps,
		InsecureAuth: true,
		Logger:       log.New(io.Discard, "", 0),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return fake{host: "127.0.0.1", port: ln.Addr().(*net.TCPAddr).Port}
}

var withIdle = imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}}

func (f fake) account() Account {
	return Account{Name: "home", Host: f.host, Port: f.port, User: "me@example.org", Password: "secret", Poll: 100 * time.Millisecond}
}

// deliver appends a message the way a delivery or another client would.
func (f fake) deliver(t *testing.T, subject string, flags ...imap.Flag) {
	t.Helper()
	c, err := imapclient.DialInsecure(net.JoinHostPort(f.host, strconv.Itoa(f.port)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("me@example.org", "secret").Wait(); err != nil {
		t.Fatal(err)
	}
	msg := fmt.Sprintf("From: <a@example.org>\r\nTo: <me@example.org>\r\nSubject: %s\r\n\r\nHello\r\n", subject)
	cmd := c.Append("INBOX", int64(len(msg)), &imap.AppendOptions{Flags: flags})
	cmd.Write([]byte(msg))
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	c.Logout().Wait()
}

// run starts the watcher and returns a channel signalled after every scan, and a stop.
func run(t *testing.T, a Account, st *State, s Sender) (<-chan struct{}, func()) {
	t.Helper()
	scans := make(chan struct{}, 100)
	synced = func(string) { scans <- struct{}{} }
	idled := make(chan struct{}, 100)
	idling = func(string) { idled <- struct{}{} }
	lastIdled = idled
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, a, st, s)
	}()
	stop := func() {
		cancel()
		<-done
		synced = func(string) {}
		idling = func(string) {}
		beforeIdle = func(string) {}
	}
	t.Cleanup(stop)
	return scans, stop
}

func waitScan(t *testing.T, scans <-chan struct{}) {
	t.Helper()
	select {
	case <-scans:
	case <-time.After(5 * time.Second):
		t.Fatal("no scan")
	}
}

// lastIdled is the newest run's signal that its watcher is in IDLE.
var lastIdled <-chan struct{}

// waitIdle waits until the watcher is in IDLE: mail delivered before that may land in the gap
// between its scan and its IDLE, which only the refresh finds.
func waitIdle(t *testing.T) {
	t.Helper()
	select {
	case <-lastIdled:
	case <-time.After(5 * time.Second):
		t.Fatal("no idle")
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newState(t *testing.T) *State {
	t.Helper()
	st, err := LoadState(t.TempDir(), "home")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestFirstStartAnnouncesNothing(t *testing.T) {
	f := startServer(t, withIdle)
	for i := 0; i < 3; i++ {
		f.deliver(t, "old")
	}
	var r recorder
	st := newState(t)
	scans, _ := run(t, f.account(), st, &r)
	waitScan(t, scans)
	time.Sleep(200 * time.Millisecond)
	if n, c := r.counts(); n != 0 || len(c) != 0 {
		t.Fatalf("posted %d new mail, checks %v", n, c)
	}
	if st.Last() != 3 {
		t.Fatalf("last uid %d", st.Last())
	}
}

func TestNewMessagePosts(t *testing.T) {
	f := startServer(t, withIdle)
	var r recorder
	scans, _ := run(t, f.account(), newState(t), &r)
	waitScan(t, scans)
	waitIdle(t)
	f.deliver(t, "hello")
	eventually(t, func() bool { n, _ := r.counts(); return n == 1 })
}

func TestBurstPostsOnce(t *testing.T) {
	f := startServer(t, withIdle)
	var r recorder
	scans, _ := run(t, f.account(), newState(t), &r)
	waitScan(t, scans)
	waitIdle(t)
	for i := 0; i < 5; i++ {
		f.deliver(t, "burst")
	}
	eventually(t, func() bool { n, _ := r.counts(); return n >= 1 })
	time.Sleep(200 * time.Millisecond)
	if n, _ := r.counts(); n != 1 {
		t.Fatalf("burst posted %d times", n)
	}
}

func TestSeenAppendIsNotNewMail(t *testing.T) {
	f := startServer(t, withIdle)
	var r recorder
	scans, _ := run(t, f.account(), newState(t), &r)
	waitScan(t, scans)
	waitIdle(t)
	f.deliver(t, "my own copy", imap.FlagSeen)
	waitScan(t, scans)
	time.Sleep(200 * time.Millisecond)
	if n, _ := r.counts(); n != 0 {
		t.Fatalf("own save posted %d", n)
	}
}

func TestCheckMessagePostsCheck(t *testing.T) {
	f := startServer(t, withIdle)
	var r recorder
	scans, _ := run(t, f.account(), newState(t), &r)
	waitScan(t, scans)
	waitIdle(t)
	f.deliver(t, Marker+"AAAAAAAAAAAAAAAAAAAAAA", imap.FlagSeen, imap.FlagDeleted)
	eventually(t, func() bool { _, c := r.counts(); return len(c) == 1 })
	if n, c := r.counts(); n != 0 || c[0] != "AAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("new mail %d, checks %v", n, c)
	}
}

func TestBadNonceIsIgnored(t *testing.T) {
	f := startServer(t, withIdle)
	var r recorder
	scans, _ := run(t, f.account(), newState(t), &r)
	waitScan(t, scans)
	waitIdle(t)
	f.deliver(t, Marker+"short", imap.FlagSeen, imap.FlagDeleted)
	waitScan(t, scans)
	time.Sleep(200 * time.Millisecond)
	if n, c := r.counts(); n != 0 || len(c) != 0 {
		t.Fatalf("new mail %d, checks %v", n, c)
	}
}

func TestUIDValidityChangeAnnouncesNothing(t *testing.T) {
	f := startServer(t, withIdle)
	for i := 0; i < 3; i++ {
		f.deliver(t, "old")
	}
	st := newState(t)
	st.UIDValidity, st.LastUID = 999999, 0
	var r recorder
	scans, _ := run(t, f.account(), st, &r)
	waitScan(t, scans)
	time.Sleep(200 * time.Millisecond)
	if n, _ := r.counts(); n != 0 {
		t.Fatalf("posted %d after a UIDVALIDITY change", n)
	}
	if st.Last() != 3 {
		t.Fatalf("last uid %d", st.Last())
	}
}

func TestRestartAnnouncesOnlyNew(t *testing.T) {
	f := startServer(t, withIdle)
	f.deliver(t, "old")
	dir := t.TempDir()
	st, _ := LoadState(dir, "home")
	var r recorder
	scans, stop := run(t, f.account(), st, &r)
	waitScan(t, scans)
	stop()
	f.deliver(t, "while stopped 1")
	f.deliver(t, "while stopped 2")
	again, err := LoadState(dir, "home")
	if err != nil || again.Last() != 1 {
		t.Fatalf("reloaded state: %v %v", again, err)
	}
	run(t, f.account(), again, &r)
	eventually(t, func() bool { n, _ := r.counts(); return n == 1 })
	time.Sleep(200 * time.Millisecond)
	if n, _ := r.counts(); n != 1 {
		t.Fatalf("posted %d", n)
	}
}

func TestNoIdlePolls(t *testing.T) {
	idles = func(*imapclient.Client) bool { return false }
	t.Cleanup(func() { idles = func(c *imapclient.Client) bool { return c.Caps().Has(imap.CapIdle) } })
	f := startServer(t, withIdle)
	var r recorder
	scans, _ := run(t, f.account(), newState(t), &r)
	waitScan(t, scans)
	f.deliver(t, "hello")
	eventually(t, func() bool { n, _ := r.counts(); return n == 1 })
}

func TestPlainRefusedOffLoopback(t *testing.T) {
	a := Account{Name: "x", Host: "mail.example.org", Port: 143, User: "u", Password: "p"}
	if err := Run(context.Background(), a, &State{}, &recorder{}); err == nil {
		t.Fatal("plain connection off loopback allowed")
	}
}

// A server with a certificate from the owner's own CA is reached over TLS with RootCAs.
func TestTLSWithOwnCA(t *testing.T) {
	caPool, serverCert := testCertificates(t)
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("me@example.org", "secret")
	user.Create("INBOX", nil)
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:   withIdle,
		Logger: log.New(io.Discard, "", 0),
	})
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{serverCert}})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	a := Account{
		Name: "tls", Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, TLS: "tls",
		User: "me@example.org", Password: "secret", RootCAs: caPool, Poll: time.Second,
	}
	var r recorder
	scans, _ := run(t, a, newState(t), &r)
	waitScan(t, scans)
}

func testCertificates(t *testing.T) (*x509.CertPool, tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// A post that fails is not lost: the mail is announced again once the relay answers.
func TestFailedPostIsTriedAgain(t *testing.T) {
	firstBackoff = 10 * time.Millisecond
	t.Cleanup(func() { firstBackoff = time.Second })
	f := startServer(t, withIdle)
	r := recorder{failNext: 1}
	st := newState(t)
	scans, _ := run(t, f.account(), st, &r)
	waitScan(t, scans)
	waitIdle(t)
	before := st.Last()
	f.deliver(t, "hello")
	eventually(t, func() bool { n, _ := r.counts(); return n == 1 })
	if r.tries != 2 || st.Last() != before+1 {
		t.Fatalf("tries %d, last %d", r.tries, st.Last())
	}
}

func TestBackoffResetsOnlyAfterAStableSession(t *testing.T) {
	if got := nextWait(4*time.Second, time.Second); got != 8*time.Second {
		t.Fatalf("short session: %v", got)
	}
	if got := nextWait(4*time.Second, 2*stable); got != firstBackoff {
		t.Fatalf("stable session: %v", got)
	}
	if got := nextWait(maxBackoff, 0); got != maxBackoff {
		t.Fatalf("capped: %v", got)
	}
}

// Mail that lands between a scan and the IDLE after it is told of by no server notice here: the
// IDLE ends now and then, and the scan after it finds the mail.
func TestMailBetweenScanAndIdleIsFoundOnTheRefresh(t *testing.T) {
	old := idleRefresh
	idleRefresh = 300 * time.Millisecond
	t.Cleanup(func() { idleRefresh = old })
	f := startServer(t, withIdle)
	var once sync.Once
	beforeIdle = func(string) { once.Do(func() { f.deliver(t, "in the gap") }) }
	var r recorder
	run(t, f.account(), newState(t), &r)
	eventually(t, func() bool { n, _ := r.counts(); return n == 1 })
}
