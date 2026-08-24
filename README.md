# vibepanel

A web console for running many parallel coding-agent sessions.

tmux keeps the processes alive. The browser owns everything else: how sessions
are grouped into projects, what they are called, which ones need you right now,
and what order they appear in.

> Status: usable, not finished. Terminals, session state, projects, scratch
> terminals, the side panel, authentication and TLS all work. Still to come:
> the mobile layout and its soft keyboard, a settings page, and the packaging
> and release pipeline.

## The problem

Running a dozen coding agents at once, a terminal multiplexer gives you a flat
strip of tabs called `bash`. You cannot tell which agent is waiting on a
confirmation and which is still working without clicking into each one, tabs
belonging to the same project are scattered among tabs from five other
projects, and none of it is usable from a phone.

That is a task-management problem wearing a terminal costume. vibepanel splits
the two apart: tmux does process persistence, and the web UI does organisation.

## Design

**The page is a view, not the state.** Close it, open it in three places at
once, reload it mid-command — the sessions never notice. All state lives in the
backend and is broadcast to every connected client.

**The backend is a client too.** Sessions are tmux sessions on a dedicated
socket. Restarting, upgrading or crashing the panel does not touch them,
because the processes are children of the tmux server. This is the single
most important property of the system and everything else is arranged
around it.

**One authoritative grid per session.** A desktop at 200×50 and a phone at
45×20 cannot both size the same terminal. Rather than reflowing — which turns
an agent's TUI into confetti — the panel keeps one grid size owned by whoever
last interacted, and other viewers scale it to fit. Everyone sees the same
bytes in the same grid.

**Three states, one definition.** A session is *working*, *waiting for you*, or
*done*. Waiting is the only one that costs you anything when unnoticed, so it
sorts first and is the loudest thing on the screen. States come from the
agent itself where possible (an optional hook), and from the output stream
otherwise. The hook is never a prerequisite.

## Requirements

- tmux 3.2 or newer (`apt install tmux`)
- Nothing else. The release binary is static and self-contained.

## Try it

```sh
go build -o vibepanel ./cmd/vibepanel

./vibepanel doctor
./vibepanel project add --path ~/projects/example
./vibepanel session ls
```

`doctor` verifies the environment and asserts that the panel's tmux socket
contains no sessions but its own — it is designed to be installed next to an
existing tmux or zellij setup without disturbing it.

## Configuration

Every flag has a `VIBEPANEL_<UPPER_SNAKE>` environment equivalent. Flags win.

| Flag | Default | Notes |
|---|---|---|
| `--data-dir` | `~/.local/share/vibepanel` | database, tmux config, ACME state |
| `--addr` | `:8443` | listen address |
| `--domain` | — | public hostname; also the WebAuthn Relying Party ID |
| `--tls` | `off` | `off`, `files`, or `acme` |
| `--tls-cert` / `--tls-key` | — | for `--tls files`; reloaded on change |
| `--acme-dns` | — | DNS-01 provider for `--tls acme` (currently `cloudflare`) |
| `--acme-email` | — | contact address for the CA |
| `--acme-directory` | Let's Encrypt | point at a staging endpoint while testing |
| `--allow-from` | — | comma-separated CIDRs allowed to reach the panel |
| `--tmux-socket` | `vibepanel` | keep it dedicated to stay isolated |
| `--static-dir` | — | serve the frontend from disk instead of the embedded build |

### Signing in

First run prints a one-time setup token to the console. That is the handover:
whoever can read the server's output is entitled to claim the panel, and merely
reaching it over the network is not. The setup endpoint closes permanently once
an account exists.

Everything except the health probe and the agent-hook endpoint needs a session,
the WebSocket included — it is the terminal itself.

### Passkeys

WebAuthn requires a secure context and a Relying Party ID that is a registrable
domain name. **An IP address is never a valid RP ID**, so `https://192.168.1.10:8443`
cannot register a passkey no matter how the TLS is arranged. Use a hostname.

Password login always works and is set up on first run. Passkeys are an
addition on top of it, never the only way in. Both `vibepanel doctor` and the
sign-in screen report whether the current configuration supports them, and say
why not if it does not.

### Certificates

```sh
# your own certificate, reloaded when it changes
vibepanel --domain panel.example.com --tls files \
          --tls-cert /etc/ssl/panel.pem --tls-key /etc/ssl/panel.key

# or issued and renewed automatically
CLOUDFLARE_API_TOKEN=... vibepanel --domain panel.example.com \
          --tls acme --acme-dns cloudflare --acme-email you@example.com
```

HTTP-01 validation needs port 80, which this panel does not expect to have, so
automatic certificates use DNS-01.

## Contributing

`AGENTS.md` has the conventions and the red lines. `docs/build-log.md` records
what was built and what went wrong along the way — including a tmux 3.6 crash
that shaped one of the core design details.

## License

MIT
