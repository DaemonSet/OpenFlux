# OpenFlux MAX debugging environment

You are working directly on a dedicated Ubuntu 22.04 VPS as root.

Goal:
Make OpenFlux MAX/OneMe transport work end-to-end:
SOCKS5 client -> MAX transport -> exit node -> Internet.

Repository:
- /root/OpenFlux-Android

Runtime/build directory:
- /root/openflux

Sensitive credentials:
- /root/openflux/max-exit-token
- /root/openflux/max-client-token

NEVER print, cat, log, echo, commit, or expose token contents.

Other services exist on this VPS:
- Amnezia
- Xray
- Docker
- Dante
- SSH

Do not modify or stop unrelated services.

Testing:
- Exit-node account token: /root/openflux/max-exit-token
- Client account token: /root/openflux/max-client-token
- SOCKS test listener: 127.0.0.1:1081
- VPS public IP expected through tunnel: 213.218.212.12

The exit node requires:
iptables OUTPUT TCP RST DROP while testing raw TCP.

Do not persist firewall changes without explicit approval.

Current MAX debugging state:
- MAX WebSocket connection/login works for both accounts.
- opcode 78 is used to initiate calls.
- Current failure returned by MAX:
  rejectedParticipants:
  errorCode = privacy.violation
- We passed --maxUid 430846376, obtained from browser localStorage viewerId.
- MAX returned rejected participant id 441226311.
- Therefore viewerId may not equal profile.contact.id used by calls.
- First determine profile.contact.id returned by opcode 19 for both accounts.
- Do not assume either ID is correct until verified.

Relevant files:
- transport/oneme/max_wclient.go
- transport/oneme/max_call.go
- transport/oneme/max_transport.go

Known code quality problems:
- OneMeTransport.Start ignores Connect/LoginByToken errors.
- opcode 78 response validation was originally missing.
- Transport is experimental.

Build:
go build -o /root/openflux/openflux-debug .

Use tmux for end-to-end testing.
There are dedicated panes for exit node and client.
You may use tmux send-keys and capture-pane to control/read them.

Work iteratively:
inspect -> patch -> gofmt -> build -> run -> inspect logs -> fix.

Do not merely suggest patches to the user when you can safely apply and test them yourself.
Explain significant findings and avoid unrelated refactors.

## CRITICAL VPS SAFETY RULES

This VPS contains production/important unrelated networking services.

DO NOT:
- stop, restart, modify, remove, inspect credentials of, or reconfigure Docker containers
- modify Amnezia VPN, Xray, AWG, Dante, dnstt, SSH
- run docker, docker compose, systemctl, service, nft, ufw
- modify routes, addresses, interfaces, sysctls, DNS configuration, or forwarding
- touch amn0, docker0, ens3
- kill processes unless they are OpenFlux processes started by you
- modify files under /etc or unrelated application directories
- flush, replace, or reorder firewall rules
- use iptables-save/restore to apply changes

The ONLY firewall mutation allowed for OpenFlux testing is exactly:
iptables -C OUTPUT -p tcp --tcp-flags RST RST -j DROP 2>/dev/null || iptables -I OUTPUT 1 -p tcp --tcp-flags RST RST -j DROP

And when cleanup is needed, only:
iptables -D OUTPUT -p tcp --tcp-flags RST RST -j DROP

Before any other system/network/firewall change, STOP and ask the user for approval.

Ports/services that must remain untouched include:
- UDP 53: dnstt
- TCP/UDP 443: Amnezia Xray
- UDP 46876: Amnezia AWG
- TCP 1080: Dante

Only OpenFlux port 1081 is available for the client test.
