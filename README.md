# akpa

Share a folder on your machine over a single link. No account, no config, no port forwarding.

```console
$ cd ~/photos
$ akpa

   ___   __ _____  ___
  / _ | / //_/ _ \/ _ |
 / __ |/ ,< / ___/ __ |
/_/ |_/_/|_/_/  /_/ |_|
  share a folder over one link  ·  v1.0.0

  … starting file server  http://127.0.0.1:39465 ✓
  … connecting to relay akpa.victorabuka.com:7000 ✓
  … waiting for tunnel id  hdy2yuyh63hhdh ✓

  serving  /home/vic/photos
  link     https://akpa.victorabuka.com/live/hdy2yuyh63hhdh

  ready. ctrl-c to stop.
```

Open that link on any device, anywhere, and you get the contents of `~/photos`.

---

## Install

```sh
curl -fsSL https://akpa.victorabuka.com/install.sh | bash
```

Detects your OS and CPU, verifies a SHA-256 checksum, and drops a single static binary in `~/.local/bin`.

If piping a script into your shell makes you uneasy — reasonably — read it first:

```sh
curl -fsSL https://akpa.victorabuka.com/install.sh -o install.sh
less install.sh
bash install.sh
```

**Other ways in:**

| | |
| --- | --- |
| Specific version | `AKPA_VERSION=v1.2.0 curl -fsSL https://akpa.victorabuka.com/install.sh \| bash` |
| Custom location | `AKPA_INSTALL_DIR=/usr/local/bin curl -fsSL … \| bash` |
| Manual | Grab a binary from [Releases](https://github.com/Abuka-Victor/akpa-cli/releases) |

Supported: macOS (Intel + Apple Silicon), Linux (x86-64 + ARM64). Windows binaries are published but untested.

## Usage

```
akpa [flags] [directory]
```

| Flag | Default | Description |
| --- | --- | --- |
| `--relay` | `akpa.victorabuka.com:7000` | Relay to connect through |
| `--port` | `0` | Local file-server port; `0` picks a free one |
| `--download` | off | Force files to download instead of rendering in the browser |
| `--no-banner` | off | Skip the startup banner |
| `--version` | | Print version and exit |

```sh
akpa                              # serve the current directory
akpa ~/Downloads                  # serve somewhere else
akpa --port 5174 ~/photos         # pin the local port
akpa --relay localhost:8080       # point at a relay running on this machine
```

## Configuration

Settings resolve in this order, with later sources winning:

```
defaults  <  ~/.config/akpa/config  <  environment variables  <  flags
```

To set a permanent default, create `~/.config/akpa/config`:

```ini
AKPA_RELAY=akpa.victorabuka.com:7000
# AKPA_PORT=5174
# AKPA_NO_BANNER=1
# AKPA_DOWNLOAD=1
```

| Variable | Effect |
| --- | --- |
| `AKPA_RELAY` | Relay address |
| `AKPA_PORT` | Local port |
| `AKPA_NO_BANNER` | Any value hides the banner |
| `AKPA_DOWNLOAD` | Any value forces downloads |
| `AKPA_DEV` | Any value makes akpa also read `./.env` |
| `NO_COLOR` | Any value disables colored output |

> akpa deliberately does **not** read `./.env` by default. It runs in whatever
> directory you are sharing, so picking up a stray `.env` from a cloned repo or
> a downloads folder would silently change your settings. Set `AKPA_DEV=1`
> during development if you want that behaviour.

## Updating

```sh
akpa upgrade
```

Re-runs the installer, which fetches the newest release and replaces the binary where it already lives. Silence the startup update check with `AKPA_NO_UPDATE_CHECK=1`.

## Uninstalling

```sh
rm ~/.local/bin/akpa
rm -rf ~/.config/akpa      # only if you created a config file
```

## How it works

```
   your machine                     akpa relay                    visitor
┌─────────────────┐            ┌──────────────────┐         ┌──────────────┐
│  akpa CLI       │            │  akpa server     │         │  browser     │
│                 │            │                  │         │              │
│  file server ───┼──┐         │  :7000  control  │         │              │
│  on 127.0.0.1   │  │         │  :8081  public   │         │              │
└─────────────────┘  │         └──────────────────┘         └──────────────┘
                     │                  ▲                          │
                     └──── outbound ────┘                          │
                          TCP, held open                           │
                                        ▲                          │
                                        └── GET /live/{id} ────────┘
```

The CLI dials **out** to the relay and keeps that connection open. Because it is
outbound, it works from behind NAT, CGNAT, dorm WiFi, and office firewalls that
drop everything inbound.

When someone opens your link, the relay looks up which connection owns that ID,
forwards the HTTP request down it, and streams the response back. Your machine
never accepts an inbound connection.

## Security

- **The link is the credential.** IDs are random and long, but anyone holding the
  URL gets in. Share it like a password.
- **Tunnels are ephemeral.** Stop the process and the link dies. Restarting gives
  you a new ID.
- **Nothing is stored.** The relay proxies bytes; it does not save your files.
- Serve only what you mean to. `akpa` in `~` shares your entire home directory.

## Building from source

```sh
git clone https://github.com/Abuka-Victor/akpa-cli
cd akpa-cli
go build -o akpa .
```

With version metadata, the way releases are built:

```sh
go build -ldflags "-s -w \
  -X main.version=$(git describe --tags --always) \
  -X main.commit=$(git rev-parse HEAD) \
  -X main.date=$(date -u +%Y-%m-%d)" \
  -o akpa .
```

## Releasing

See [RELEASE.md](RELEASE.md).

## License

MIT