# Outwright IMAP push worker (example)

> **This is an example, not a supported product.** What Outwright supports is the push address
> and its protocol ([outwright.app/push/worker](https://outwright.app/push/worker)). This worker
> is a working starting point you build, adapt and run yourself. Fixes and pull requests are
> welcome, but there is no promise it fits your server or your setup.

Outwright gets push from Stalwart (JMAP), Dovecot (with Outwright's script) and Microsoft 365.
For any other IMAP server you can run a sender of your own that tells Outwright when mail
arrives. This is one: a small program that keeps an IMAP connection on each configured Inbox
and posts to the account's push address when new mail comes in.

- Your password stays on your machine. The push address carries nothing about your account,
  and what is posted to it carries nothing about your mail: Outwright fetches the mail itself.
- IMAP IDLE where the server has it (renewed every 25 minutes, as RFC 2177 asks), polling where it does not.
- A first start, a changed UIDVALIDITY, your own saved copies (already `\Seen`) and Outwright's
  check message never notify. The last UID seen is kept, so a restart announces nothing old.
- TLS or STARTTLS only.

## Build

You build the image yourself; none is published.

```sh
docker build -t outwright-imap-push .
# or for another platform:
docker buildx build --platform linux/arm64 -t outwright-imap-push .
```

Without Docker: `make build` (Go 1.27 or later) makes `bin/outwright-imap-push`.

## Configure

1. In Outwright, open the account's settings and choose **Push for a server without push**,
   under the Notifications switch while push from the server is not on. Copy the push address it
   shows.
2. Copy `config.example.toml` to `config/config.toml` and fill in one `[[account]]` per mailbox:
   server, port, `tls` (`"tls"` or `"starttls"`), user, `password` or `password_file`, and the
   push address. A server whose certificate comes from a CA of your own takes `ca_file`.
3. Start the worker (below), then tap **Check** in Outwright. Push turns on when the check
   comes back through your worker.

On a second device with Outwright, choose **Paste an address your worker already uses** and
paste the same address; the worker needs no change.

## Run

```sh
cp compose.example.yml compose.yml
docker compose up -d --build
```

Or plain Docker:

```sh
docker run -d --name outwright-imap-push --restart unless-stopped \
  -v "$PWD/config:/config:ro" -v outwright-imap-push-state:/state outwright-imap-push
```

The worker runs as an unprivileged user (uid 65532). A named volume for `/state`, as above, is
writable by it; if you bind-mount a directory instead, give it to that user first
(`chown 65532 state`), or the worker cannot keep what it has seen.

The config path is `-config` or `OUTWRIGHT_IMAP_PUSH_CONFIG` (default `/config/config.toml`),
the state directory `-state` or `OUTWRIGHT_IMAP_PUSH_STATE` (default `/state`). Logs go to
standard error and never contain a password.

When push is switched off in Outwright, the address goes away (the relay answers `404`); the
worker logs that and stops watching that account until it is restarted. A post that fails for
another reason (the relay cannot be reached) is not lost: the worker reconnects and posts again.

## The protocol, in short

- `POST <push address>` with an empty body: new mail. Answers `202`.
- `POST <push address>` with `check:<nonce>`: the answer to Outwright's check. The app saves a
  message to the Inbox whose subject is `Outwright push check <nonce>` (already `\Seen` and
  `\Deleted`) and removes it when the check ends.
- `404`: the address is gone. `429`: over 120 posts an hour; try later.

The full protocol: [outwright.app/push/worker](https://outwright.app/push/worker).

## Develop

`make test` runs the tests against an in-memory IMAP server; `make ci` also checks formatting
and vets.

## Licence

MIT or Apache-2.0, at your option.
