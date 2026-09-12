# OpenFlux contributor notes

## Project direction

OpenFlux is now Volga-only.

Do not reintroduce:

- MAX / OneMe transport;
- legacy pre-Volga Yandex Docs transport;
- host-wide TCP RST suppression.

## Current transport stack

```text
Application / Android TUN
        |
      DNSMux
        |
 multi-client OFM2
        |
    compression
        |
   AES-256-GCM
        |
 resilient Yandex Volga
```

## Android

The Android client currently tunnels IPv4/TCP.

DNS packets are intercepted by `VpnService` and forwarded through DNSMux to the
exit node.

General UDP/QUIC and IPv6 are not implemented yet.

## Linux exit node

The raw TCP exit should run inside a dedicated Linux network namespace.

TCP RST suppression must be namespace-local.

Never add a host-wide rule such as:

```bash
iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP
```

## Production safety

Treat existing host networking and unrelated services as out of scope.

Before changing a production host:

- prefer read-only inspection first;
- do not stop, restart or reconfigure unrelated services;
- do not apply broad firewall, routing, interface, DNS or sysctl changes;
- keep OpenFlux-specific network and firewall changes inside its dedicated
  network namespace whenever possible;
- never print, log or commit document URLs, encryption secrets or other
  credentials;
- require explicit operator approval before making unrelated host-level
  changes.

## Development checks

Before committing Go changes:

```bash
gofmt -w $(git ls-files '*.go')
go test ./...
go vet ./...
(cd mobile && go test ./...)
git diff --check
```

For Android-related changes also build the debug APK:

```bash
BUILD_TYPE=debug ./build_android_app.sh
```
