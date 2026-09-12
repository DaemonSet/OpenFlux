# Fork information

This repository is an independently maintained derivative of
[p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux).

The original project is the architectural foundation for the tunnel, gVisor
network stack integration, raw-socket exit node and transport abstraction.

This repository was initialized from a working source tree rather than as a
GitHub fork, so the original upstream commit history is not embedded in this
Git repository. Upstream attribution and GPL licensing are preserved.

## Current direction

This fork is focused exclusively on the Yandex Volga transport.

The legacy MAX/OneMe transport and the older pre-Volga Yandex Docs
transport have been removed. The current codebase is Volga-only.

## Major changes

- native Android `VpnService` client;
- Android Keystore-backed storage for document URL and encryption secret;
- mandatory AES-256-GCM transport encryption with scrypt key derivation;
- compression layer;
- multi-client OFM2 routing;
- userspace source-port NAT for simultaneous clients;
- DNSMux control channel through the exit node;
- AdGuard DNS filtering with fallback resolver;
- resilient Yandex Volga sessions with reconnect and full re-auth;
- isolated Linux network-namespace deployment for the exit node;
- Android ping/latency diagnostics;
- multi-client, DNS, NAT and Volga recovery tests.

## Limitations

The Android tunnel currently supports IPv4/TCP. DNS is handled separately
through DNSMux. General UDP/QUIC and IPv6 support are planned but are not yet
implemented.

Multiple clients currently share one transport encryption secret. Per-client
session keys are planned for a future protocol version.

OpenFlux is experimental software and is not affiliated with or endorsed by
Yandex.
