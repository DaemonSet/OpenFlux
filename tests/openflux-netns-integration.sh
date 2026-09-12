#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
    echo "ERROR: integration test must run as root inside an isolated namespace" >&2
    exit 1
fi

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)"
NETNS_SCRIPT="${REPO_ROOT}/deploy/openflux-netns"
CONFIG_FILE="/run/openflux-test.conf"

ETC_NETNS_CREATED=0

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

pass() {
    echo "PASS: $*"
}

cleanup() {
    set +e

    OPENFLUX_CONFIG="${CONFIG_FILE}" \
        "${NETNS_SCRIPT}" down >/dev/null 2>&1

    umount /etc/netns >/dev/null 2>&1

    if [[ "${ETC_NETNS_CREATED}" == "1" ]]; then
        rmdir /etc/netns >/dev/null 2>&1
    fi
}

prepare_resolv_target() {
    local target

    if [[ ! -L /etc/resolv.conf ]]; then
        return 0
    fi

    target="$(readlink /etc/resolv.conf)"

    case "${target}" in
        /run/*)
            ;;
        ../run/*)
            target="/run/${target#../run/}"
            ;;
        *)
            return 0
            ;;
    esac

    mkdir -p "$(dirname -- "${target}")"
    printf 'nameserver 1.1.1.1\n' >"${target}"
}

check_topology() {
    ip netns list |
        awk '{print $1}' |
        grep -qx openflux ||
        fail "openflux namespace missing"

    ip -4 addr show dev of-host |
        grep -Fq '10.203.0.1/30' ||
        fail "host veth address missing"

    ip netns exec openflux \
        ip -4 addr show dev of-ns |
        grep -Fq '10.203.0.2/30' ||
        fail "namespace veth address missing"

    ip netns exec openflux \
        ip route show default |
        grep -Fq 'default via 10.203.0.1 dev of-ns' ||
        fail "namespace default route missing"
}

check_rules() {
    local wan="$1"

    iptables -w 5 -C FORWARD \
        -i of-host -o "${wan}" \
        -s 10.203.0.2/32 \
        -j ACCEPT 2>/dev/null ||
        fail "outbound FORWARD rule missing on ${wan}"

    iptables -w 5 -C FORWARD \
        -i "${wan}" -o of-host \
        -d 10.203.0.2/32 \
        -m conntrack --ctstate ESTABLISHED,RELATED \
        -j ACCEPT 2>/dev/null ||
        fail "return FORWARD rule missing on ${wan}"

    iptables -w 5 -t nat -C POSTROUTING \
        -s 10.203.0.0/30 \
        -o "${wan}" \
        -j MASQUERADE 2>/dev/null ||
        fail "MASQUERADE rule missing on ${wan}"

    ip netns exec openflux \
        iptables -w 5 -C OUTPUT \
        -p tcp \
        --tcp-flags RST RST \
        -j DROP 2>/dev/null ||
        fail "namespace-local TCP RST rule missing"
}

check_no_old_wan_rules() {
    local wan="$1"

    if iptables -w 5 -C FORWARD \
        -i of-host -o "${wan}" \
        -s 10.203.0.2/32 \
        -j ACCEPT 2>/dev/null
    then
        fail "old outbound FORWARD rule remains on ${wan}"
    fi

    if iptables -w 5 -C FORWARD \
        -i "${wan}" -o of-host \
        -d 10.203.0.2/32 \
        -m conntrack --ctstate ESTABLISHED,RELATED \
        -j ACCEPT 2>/dev/null
    then
        fail "old return FORWARD rule remains on ${wan}"
    fi

    if iptables -w 5 -t nat -C POSTROUTING \
        -s 10.203.0.0/30 \
        -o "${wan}" \
        -j MASQUERADE 2>/dev/null
    then
        fail "old MASQUERADE rule remains on ${wan}"
    fi
}

check_single_rule_set() {
    local forward_count
    local nat_count
    local rst_count

    forward_count="$(
        iptables -S FORWARD |
            grep -c -- 'of-host' || true
    )"

    nat_count="$(
        iptables -t nat -S POSTROUTING |
            grep -c -- '10.203.0.0/30' || true
    )"

    rst_count="$(
        ip netns exec openflux \
            iptables -S OUTPUT |
            grep -c -- '--tcp-flags RST RST' || true
    )"

    [[ "${forward_count}" == "2" ]] ||
        fail "FORWARD rule count=${forward_count}, want 2"

    [[ "${nat_count}" == "1" ]] ||
        fail "NAT rule count=${nat_count}, want 1"

    [[ "${rst_count}" == "1" ]] ||
        fail "namespace RST rule count=${rst_count}, want 1"
}

trap cleanup EXIT

mount --make-rprivate /
mount -t tmpfs -o mode=0755 tmpfs /run

mkdir -p /run/netns

prepare_resolv_target

if [[ ! -d /etc/netns ]]; then
    mkdir -p /etc/netns
    ETC_NETNS_CREATED=1
fi

mount -t tmpfs -o mode=0755 tmpfs /etc/netns

echo '=== CREATE SANDBOX WAN INTERFACES ==='

ip link add wan0 type dummy
ip link add wan1 type dummy

ip addr add 192.0.2.1/24 dev wan0
ip addr add 198.51.100.1/24 dev wan1

ip link set wan0 up
ip link set wan1 up

ip -4 route replace default dev wan0

rm -f "${CONFIG_FILE}"

echo
echo '=== FIRST UP / AUTO-DETECT WAN0 ==='

OPENFLUX_CONFIG="${CONFIG_FILE}" \
    "${NETNS_SCRIPT}" up

check_topology
check_rules wan0

[[ "$(cat /run/openflux/netns-wan-if)" == "wan0" ]] ||
    fail "saved WAN state is not wan0"

pass "first up with automatic WAN detection"

echo
echo '=== SECOND UP / IDEMPOTENCY ==='

OPENFLUX_CONFIG="${CONFIG_FILE}" \
    "${NETNS_SCRIPT}" up

check_topology
check_rules wan0
check_single_rule_set

pass "second up did not duplicate rules"

echo
echo '=== REPAIR BROKEN VETH ==='

ip link del of-host

if ip link show dev of-host >/dev/null 2>&1; then
    fail "of-host still exists after intentional deletion"
fi

OPENFLUX_CONFIG="${CONFIG_FILE}" \
    "${NETNS_SCRIPT}" up

check_topology
check_rules wan0
check_single_rule_set

pass "broken veth topology repaired"

echo
echo '=== SWITCH WAN0 -> WAN1 ==='

printf '%s\n' 'WAN_IF=wan1' >"${CONFIG_FILE}"

OPENFLUX_CONFIG="${CONFIG_FILE}" \
    "${NETNS_SCRIPT}" up

check_topology
check_rules wan1
check_no_old_wan_rules wan0
check_single_rule_set

[[ "$(cat /run/openflux/netns-wan-if)" == "wan1" ]] ||
    fail "saved WAN state is not wan1"

pass "WAN switch cleaned old rules"

echo
echo '=== DOWN ==='

OPENFLUX_CONFIG="${CONFIG_FILE}" \
    "${NETNS_SCRIPT}" down

if ip netns list |
    awk '{print $1}' |
    grep -qx openflux
then
    fail "openflux namespace remains after down"
fi

if ip link show dev of-host >/dev/null 2>&1; then
    fail "of-host remains after down"
fi

if iptables -S FORWARD | grep -q -- 'of-host'; then
    fail "OpenFlux FORWARD rules remain after down"
fi

if iptables -t nat -S POSTROUTING |
    grep -q -- '10.203.0.0/30'
then
    fail "OpenFlux NAT rule remains after down"
fi

if [[ -e /run/openflux/netns-wan-if ]]; then
    fail "WAN state remains after down"
fi

pass "down removed namespace, veth, firewall rules and state"

echo
echo '========================================'
echo 'ALL OPENFLUX NETNS INTEGRATION TESTS PASS'
echo '========================================'
