// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadExample(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "home", "pw\n")
	cfg := write(t, dir, "c.toml", strings.Replace(string(b), "/run/secrets/home", filepath.Join(dir, "home"), 1))
	accounts, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Watch.Host != "mail.example.org" || accounts[0].Watch.Poll != 5*time.Minute {
		t.Fatalf("accounts %+v", accounts)
	}
}

func TestPasswordFile(t *testing.T) {
	dir := t.TempDir()
	pw := write(t, dir, "pw", "s3cret\n")
	cfg := write(t, dir, "c.toml", `[[account]]
name = "a"
host = "mail.example.org"
port = 993
tls = "tls"
user = "u"
password_file = "`+pw+`"
push_address = "https://push.outwright.app/v1/channels/x/plain"
`)
	accounts, err := Load(cfg)
	if err != nil || accounts[0].Watch.Password != "s3cret" {
		t.Fatalf("%+v %v", accounts, err)
	}
	if accounts[0].Watch.Poll != DefaultPoll {
		t.Fatalf("poll %v", accounts[0].Watch.Poll)
	}
}

func TestRejects(t *testing.T) {
	good := map[string]string{
		"name": `"a"`, "host": `"mail.example.org"`, "port": "993", "tls": `"tls"`, "user": `"u"`,
		"password": `"topsecret"`, "push_address": `"https://push.outwright.app/v1/channels/x/plain"`,
	}
	render := func(m map[string]string) string {
		var b strings.Builder
		b.WriteString("[[account]]\n")
		for k, v := range m {
			if v != "" {
				b.WriteString(k + " = " + v + "\n")
			}
		}
		return b.String()
	}
	with := func(k, v string) string {
		m := map[string]string{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		return render(m)
	}
	cases := map[string]string{
		"no host":       with("host", ""),
		"both":          with("password_file", `"/x"`),
		"neither":       with("password", ""),
		"http address":  with("push_address", `"http://push.example.org/v1/channels/x/plain"`),
		"bad port":      with("port", "70000"),
		"bad tls":       with("tls", `"none"`),
		"duplicate":     render(good) + render(good),
		"no accounts":   "",
		"unknown field": with("passwrd", `"x"`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := write(t, t.TempDir(), "c.toml", body)
			_, err := Load(cfg)
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), "topsecret") {
				t.Fatalf("error names the password: %v", err)
			}
		})
	}
}

func TestCAFile(t *testing.T) {
	dir := t.TempDir()
	pemPath := write(t, dir, "ca.pem", string(testCAPEM(t)))
	body := `[[account]]
name = "a"
host = "mail.example.org"
port = 993
tls = "tls"
user = "u"
password = "p"
ca_file = "` + pemPath + `"
push_address = "https://push.outwright.app/v1/channels/x/plain"
`
	accounts, err := Load(write(t, dir, "c.toml", body))
	if err != nil || accounts[0].Watch.RootCAs == nil {
		t.Fatalf("%+v %v", accounts, err)
	}
	bad := write(t, dir, "bad.pem", "not a certificate")
	if _, err := Load(write(t, dir, "c2.toml", strings.Replace(body, pemPath, bad, 1))); err == nil {
		t.Fatal("accepted a ca_file without a certificate")
	}
}

func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
