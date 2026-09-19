package tunnel

import (
	"sync"
	"time"
)

const (
	rawNATPortMin         = uint16(10000)
	rawNATPortMax         = uint16(29999)
	rawNATIdle            = 30 * time.Minute
	rawNATCleanupInterval = 30 * time.Second
)

type rawFlowKey struct {
	clientIP   [4]byte
	clientPort uint16
	remoteIP   [4]byte
	remotePort uint16
}

type rawNATFlow struct {
	key        rawFlowKey
	egressPort uint16
	lastSeen   time.Time
}

type rawNATTable struct {
	mu          sync.Mutex
	byClient    map[rawFlowKey]*rawNATFlow
	byEgress    map[uint16]*rawNATFlow
	nextPort    uint16
	nextCleanup time.Time
}

func newRawNATTable() *rawNATTable {
	return &rawNATTable{
		byClient: make(map[rawFlowKey]*rawNATFlow),
		byEgress: make(map[uint16]*rawNATFlow),
		nextPort: rawNATPortMin,
	}
}

func (t *rawNATTable) outbound(key rawFlowKey, now time.Time) (uint16, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupIfDueLocked(now)
	if flow, ok := t.byClient[key]; ok {
		flow.lastSeen = now
		return flow.egressPort, true
	}

	span := int(rawNATPortMax-rawNATPortMin) + 1
	if len(t.byEgress) >= span {
		return 0, false
	}
	for i := 0; i < span; i++ {
		port := t.nextPort
		t.nextPort++
		if t.nextPort > rawNATPortMax {
			t.nextPort = rawNATPortMin
		}
		if _, used := t.byEgress[port]; used {
			continue
		}
		flow := &rawNATFlow{key: key, egressPort: port, lastSeen: now}
		t.byClient[key] = flow
		t.byEgress[port] = flow
		return port, true
	}
	return 0, false
}

func (t *rawNATTable) inbound(egressPort uint16, remoteIP [4]byte, remotePort uint16, now time.Time) (rawFlowKey, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupIfDueLocked(now)
	flow, ok := t.byEgress[egressPort]
	if !ok || flow.key.remoteIP != remoteIP || flow.key.remotePort != remotePort {
		return rawFlowKey{}, false
	}
	flow.lastSeen = now
	return flow.key, true
}

func (t *rawNATTable) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byClient)
}

func (t *rawNATTable) cleanupIfDueLocked(now time.Time) {
	if !t.nextCleanup.IsZero() && now.Before(t.nextCleanup) {
		return
	}
	t.cleanupLocked(now)
	t.nextCleanup = now.Add(rawNATCleanupInterval)
}

func (t *rawNATTable) cleanupLocked(now time.Time) {
	for key, flow := range t.byClient {
		if now.Sub(flow.lastSeen) <= rawNATIdle {
			continue
		}
		delete(t.byClient, key)
		delete(t.byEgress, flow.egressPort)
	}
}
