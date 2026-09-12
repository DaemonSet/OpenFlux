<div align="center">
  <img src="design/logo/avatar.svg" width="112" alt="OpenFlux logo">
  <h1>OpenFlux</h1>
  <p>Encrypted multi-client VPN tunnel over Yandex Volga for Android, desktop clients and Linux exit nodes.</p>
  <p>
    <a href="https://github.com/DaemonSet/OpenFlux/releases/latest"><img src="https://img.shields.io/github/v/release/DaemonSet/OpenFlux?display_name=tag&amp;sort=semver&amp;style=flat-square&amp;color=7aa2f7" alt="Latest release"></a>
    <a href="https://github.com/DaemonSet/OpenFlux/actions/workflows/ci.yml"><img src="https://github.com/DaemonSet/OpenFlux/actions/workflows/ci.yml/badge.svg" alt="CI status"></a>
    <a href="LICENSE"><img src="https://img.shields.io/github/license/DaemonSet/OpenFlux?style=flat-square" alt="GPL-3.0 license"></a>
    <img src="https://img.shields.io/badge/Android-8.0%2B-3DDC84?style=flat-square&amp;logo=android&amp;logoColor=white" alt="Android 8 or newer">
  </p>
  <p>
    <img src="https://img.shields.io/badge/Go-1.26.4%2B-00ADD8?style=flat-square&amp;logo=go&amp;logoColor=white" alt="Go 1.26.4 or newer">
    <img src="https://img.shields.io/badge/Java-17-ED8B00?style=flat-square&amp;logo=openjdk&amp;logoColor=white" alt="Java 17">
    <img src="https://img.shields.io/badge/ABI-ARM64%20%7C%20ARMv7%20%7C%20x86__64%20%7C%20x86-455a64?style=flat-square" alt="Supported Android architectures">
    <img src="https://img.shields.io/badge/IPv4%20%2F%20TCP-experimental-f59e0b?style=flat-square" alt="Experimental IPv4 and TCP support">
  </p>
  <p><strong>English</strong> · <a href="README.ru.md">Русский</a></p>
</div>

![OpenFlux Android: connection, logs and settings](docs/images/openflux-android-tabs.png)

> This repository is an experimental, independently maintained fork of
> [p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux).
> See [FORK.md](FORK.md) for the differences from upstream.

OpenFlux is an experimental encrypted VPN tunnel built around Yandex Volga as
its carrier transport. This fork focuses on Android VPN clients and Linux exit
nodes, with multi-client routing, automatic Volga session recovery and
end-to-end transport encryption.

**[Download the latest Android release](https://github.com/DaemonSet/OpenFlux/releases/latest)**

```text
Android VPN or SOCKS5 client -> encrypted Volga transport -> Linux exit node -> Internet
```

## Features

- Android 8+ client using the system `VpnService` API, with ARM, ARM64, x86 and
  x86_64 builds;
- Android 11-style UI with connection controls, logs and settings;
- AES-256-GCM authenticated encryption with a key derived using scrypt;
- Android Keystore-backed storage for the document URL and shared secret;
- encrypted latency checks and a live ping graph;
- DNS multiplexing through the exit node with AdGuard DNS filtering and fallback;
- desktop SOCKS5 client and Linux exit-node modes;
- resilient Yandex Volga transport with automatic reconnect and full re-auth;
- simultaneous multi-client Android sessions over one exit node.

## Important limitations

OpenFlux is experimental research software, not an audited replacement for
WireGuard or another mature VPN. The Android tunnel currently supports IPv4 and
TCP. DNS is multiplexed through the encrypted tunnel; arbitrary UDP and IPv6 are not
tunneled. The document provider can still observe metadata such as connection
times, traffic sizes and encrypted payloads. Anyone with document edit access
can disrupt the connection.

Use the software only on systems and networks you own or are authorized to
test.

## Requirements

- Go 1.26.4 or newer for the desktop client and exit node;
- a Linux VPS/VDS with root access for the exit node;
- for Android builds: Java 17, Android SDK/API 35, Build Tools 35.0.0,
  NDK 27.0.12077973, Gradle 8.14.3 and `gomobile`;
- an editable document opened with the legacy Yandex Docs editor when using
  the Yandex transport.

## Prepare the private configuration

Create the following files locally and copy the same values to the exit node.
They are excluded by `.gitignore` and must never be committed:

```bash
printf '%s\n' 'https://your-own-document-url' > document-url
openssl rand -base64 32 > encryption-key
chmod 600 document-url encryption-key
```

The encryption secret must contain at least 16 characters. Generate a unique
random value; do not reuse a password. Rotate both the document URL and the
secret if either is exposed.

## Build the exit node and desktop client

```bash
go build -o openflux .
```

Run the Linux exit node as root:

```bash
sudo iptables -C OUTPUT -p tcp --tcp-flags RST RST -j DROP 2>/dev/null || \
  sudo iptables -I OUTPUT 1 -p tcp --tcp-flags RST RST -j DROP
sudo ./openflux --exit-node --transport yandex \
  --url-file ./document-url --encryption-key-file ./encryption-key
```

The sample [systemd unit](deploy/openflux.service) expects the binary and
private files in `/root/openflux`. Review its paths before installing it:

```bash
sudo install -d -m 700 /root/openflux
sudo install -m 755 ./openflux /root/openflux/openflux
sudo install -m 600 ./document-url ./encryption-key /root/openflux/
sudo install -m 644 deploy/openflux.service /etc/systemd/system/openflux.service
sudo systemctl daemon-reload
sudo systemctl enable --now openflux
sudo systemctl status openflux
```

Run the desktop client and configure the browser to use SOCKS5 at
`127.0.0.1:1080`:

```bash
./openflux --client --transport yandex --socks5 127.0.0.1:1080 \
  --url-file ./document-url --encryption-key-file ./encryption-key
```

Add `--debug` only when diagnosing a problem, and inspect logs before sharing
them.

## Build and install the Android app

Set `ANDROID_SDK_ROOT` (or `ANDROID_HOME`) and ensure `gomobile` and Gradle are
available, then run:

```bash
go install golang.org/x/mobile/cmd/gomobile@v0.0.0-20260908204917-8b95e45f8d3e
go install golang.org/x/mobile/cmd/gobind@v0.0.0-20260908204917-8b95e45f8d3e
gomobile init
./build_android_app.sh
```

The build creates separate APKs for `arm64-v8a`, `armeabi-v7a`, `x86_64` and
`x86`, plus `OpenFlux-android-universal-debug.apk` for devices whose architecture
is unknown. Transfer the appropriate APK to an Android 8+ device, install it,
enter your own document URL and shared secret in **Settings**, then approve
Android's VPN prompt.

Configuration survives a normal in-place app update when the application ID
and signing certificate stay the same. Clearing app data or uninstalling the
app removes it. APKs signed with a different certificate cannot update the
existing installation. CI artifacts are debug builds; APKs attached to GitHub
Releases use the project's persistent release certificate. Moving from a debug
build to the release channel requires one uninstall and therefore clears saved
settings.

See [android/README.md](android/README.md) for Android-specific details.

## Command-line flags

| Flag | Default | Description |
| --- | --- | --- |
| `--client` | off | Run the SOCKS5 client |
| `--exit-node` | off | Run the exit node (requires root) |
| `--socks5` | `:1080` | SOCKS5 listen address |
| `--transport` | `yandex` | Transport backend (`yandex` or `oneme`) |
| `--url` | empty | Inline document URL; prefer `--url-file` |
| `--url-file` | empty | Read the document URL from a file |
| `--encryption-key-file` | empty | Read the Yandex transport secret from a file |
| `--maxToken` | empty | MAX transport token |
| `--maxUid` | empty | MAX transport user ID |
| `--debug` | off | Enable verbose logging |

## Development and security

Run checks before committing:

```bash
gofmt -w $(git ls-files '*.go')
go test ./...
go vet ./...
git diff --check
```

Contributions are described in [CONTRIBUTING.md](CONTRIBUTING.md). Please read
[SECURITY.md](SECURITY.md) before reporting a vulnerability. Changes are listed
in [CHANGELOG.md](CHANGELOG.md).

## License

OpenFlux is licensed under the GNU General Public License v3.0 or later. See
[LICENSE](LICENSE), [COPYRIGHT](COPYRIGHT) and [NOTICE](NOTICE). This fork is not
endorsed by or affiliated with Yandex.
