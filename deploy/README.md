# OpenFlux Linux exit-node deployment

OpenFlux uses raw TCP packets on the Linux exit node.

The exit process must run inside an isolated network namespace so that TCP RST
suppression affects only OpenFlux traffic and does not modify the networking
behaviour of unrelated services on the host.

## Production layout

```text
OpenFlux exit process
        |
        | network namespace: openflux
        | address: 10.203.0.2/30
        |
      of-ns
        |
      veth
        |
      of-host
        |
        | host address: 10.203.0.1/30
        |
 narrow FORWARD + MASQUERADE
        |
 public network interface
        |
     Internet
```

The host forwards only the OpenFlux namespace subnet.

## TCP RST handling

Do **not** install a host-wide rule like this:

```bash
iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP
```

A global rule can affect SSH, Docker, VPNs and other services running on the
same server.

RST suppression belongs only inside the OpenFlux namespace:

```text
namespace openflux
└── OUTPUT
    └── TCP RST -> DROP
```

The host OUTPUT chain should remain unaffected.

## Runtime files

A production installation can keep its files in a directory such as:

```text
/opt/openflux-exit/
├── openflux-exit
├── document-url.txt
└── encryption-secret.txt
```

Recommended permissions:

```bash
chmod 755 /opt/openflux-exit/openflux-exit
chmod 600 /opt/openflux-exit/document-url.txt
chmod 600 /opt/openflux-exit/encryption-secret.txt
```

The document URL and encryption secret must never be committed to Git.

## Exit-node command

The current OpenFlux runtime is Volga-only, so there is no transport selector.

Run the exit binary inside the namespace:

```bash
ip netns exec openflux \
  /opt/openflux-exit/openflux-exit \
  --exit-node \
  --url-file /opt/openflux-exit/document-url.txt \
  --encryption-key-file /opt/openflux-exit/encryption-secret.txt
```

## Desktop SOCKS5 client

A desktop client can be started with:

```bash
./openflux \
  --client \
  --socks5 127.0.0.1:1080 \
  --url-file ./document-url.txt \
  --encryption-key-file ./encryption-secret.txt
```

The browser or application can then use:

```text
SOCKS5
127.0.0.1:1080
```

## Systemd

The old sample unit that modified the host-wide OUTPUT chain has been removed.

The repository will contain namespace provisioning and systemd units matching
the tested production deployment. Those files should keep all OpenFlux-specific
RST handling inside the `openflux` namespace.

## Current transport stack

```text
Android VPN / SOCKS5
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
        |
 Linux exit node
        |
     Internet
```

## Current limitations

The Android tunnel currently supports:

- IPv4;
- TCP;
- DNS through DNSMux.

Not yet implemented:

- arbitrary UDP;
- QUIC;
- IPv6;
- per-client encryption keys.

OpenFlux is experimental software and should not be treated as an audited
replacement for mature VPN implementations.
