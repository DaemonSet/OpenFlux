# Fork information

This repository is a modified fork of
[p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux).
It preserves the upstream Git history and remains licensed under GPL-3.0-or-later.

## Major changes in this fork

- native Android `VpnService` client with a three-tab Android 11-style UI;
- encrypted Android Keystore storage for the document URL and shared secret;
- mandatory end-to-end AES-256-GCM transport encryption with scrypt key derivation;
- authenticated encrypted ping frames and an animated latency graph;
- DNS-over-HTTPS handling for Android;
- safer VDS deployment using root-only URL/key files instead of process arguments;
- removal of the non-functional iOS prototype.

The fork is experimental and is not endorsed by or affiliated with Yandex.
