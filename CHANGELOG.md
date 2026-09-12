# Changelog

All notable changes to this fork are documented here.

## Unreleased

## 2.0.0-beta.1 - 2026-09-12

### Added

- repository quality gate covering `gofmt`, root and mobile Go tests/vet,
  ShellCheck, isolated Linux network-namespace integration tests and Android
  debug APK builds;
- dedicated DNS multiplexing tests for pass-through traffic, DNS request and
  response routing, invalid requests and pending-query shutdown.

### Changed

- OpenFlux runtime is now Volga-only;
- transport selection has been removed from the command-line interface;
- Linux exit-node deployment now requires namespace-local TCP RST handling;
- exit-node namespace provisioning now auto-detects the WAN interface, cleans
  up rules using persisted runtime state and repairs incomplete veth topology;
- Android application version is now 2.0.0-beta.1 (version code 20001).

### Removed

- legacy MAX/OneMe transport;
- legacy pre-Volga Yandex Docs transport;
- obsolete MAX Android bridge code;
- WebRTC dependency tree used only by MAX/OneMe;
- unsafe sample systemd unit with host-wide TCP RST suppression.

## 1.0.0 - 2026-09-12

### Added

- multi-client Android support with per-client OFM2 routing;
- DNS multiplexing for simultaneous clients;
- userspace TCP source-port NAT for identical client tuples;
- resilient Yandex Volga transport with automatic reconnect and full re-auth;
- AdGuard DNS filtering with `94.140.14.14` primary and `94.140.15.15` fallback;
- isolated Linux exit-node network namespace deployment;
- integration tests for multi-client routing, DNS multiplexing and Volga recovery.

### Changed

- Yandex Volga is the primary document transport;
- Android default DNS changed from Cloudflare to AdGuard DNS;
- Android application version is now 1.0.0 (version code 100).

### Security

- transport payloads remain protected with AES-256-GCM;
- document URL and encryption secret remain stored using Android Keystore-backed settings.

## 0.3.0 - 2026-09-10

### Added

- Android APKs for `arm64-v8a`, `armeabi-v7a`, `x86_64`, `x86` and universal
  devices;
- automated GitHub Release publishing for tags matching `v*`;
- SHA-256 checksums for every release APK.

## 0.2.0 - 2026-09-10

### Added

- native Android `VpnService` client with connection, logs and settings tabs;
- Android Keystore-backed protection for the saved document URL and secret;
- mandatory AES-256-GCM encryption for the Yandex document transport;
- encrypted ping frames and an animated latency graph;
- DNS-over-HTTPS support for the Android VPN;
- a hardened sample systemd service for the Linux exit node;
- CI checks for Go and Android debug builds.

### Changed

- secret values can be loaded from root-only files instead of command-line
  arguments;
- Android application version is now 0.2.0 (version code 2).

### Removed

- the incomplete iOS prototype and generated IDE/build artifacts.
