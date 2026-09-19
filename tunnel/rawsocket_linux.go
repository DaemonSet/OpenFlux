package tunnel

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"universal-bypass-tool/network"
	"universal-bypass-tool/utils"
)

const rawSocketErrorLogInterval = 30 * time.Second

type RawSocketEndpoint struct {
	dispatcher      stack.NetworkDispatcher
	sendFd          int
	recvFd          int
	recvMu          sync.Mutex
	closed          bool
	nicID           tcpip.NICID
	localIP         [4]byte
	packetIn        atomic.Uint64
	packetOut       atomic.Uint64
	readErrors      atomic.Uint64
	sendErrors      atomic.Uint64
	natFullEvents   atomic.Uint64
	lastReadErrLog  atomic.Int64
	lastSendErrLog  atomic.Int64
	lastNATFullLog  atomic.Int64
	nat             *rawNATTable
	sendToTransport func([]byte)
}

func NewRawSocketEndpoint(nicID tcpip.NICID) (*RawSocketEndpoint, error) {
	sendFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		return nil, fmt.Errorf("send socket failed: %v (need root)", err)
	}

	if err := syscall.SetsockoptInt(sendFd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1); err != nil {
		syscall.Close(sendFd)
		return nil, fmt.Errorf("IP_HDRINCL: %v", err)
	}
	if err := syscall.SetsockoptInt(sendFd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, 16*1024*1024); err != nil {
		utils.Debugf("[RAW-NIC%d] SO_SNDBUF tuning failed: %v", nicID, err)
	}

	recvFd, err := openRawTCPReceiveSocket(nicID)
	if err != nil {
		syscall.Close(sendFd)
		return nil, err
	}

	localIPText := getLocalIP()
	parsedLocalIP := net.ParseIP(localIPText).To4()
	if parsedLocalIP == nil {
		syscall.Close(sendFd)
		syscall.Close(recvFd)
		return nil, fmt.Errorf("invalid exit local IPv4 address %q", localIPText)
	}
	var localIP [4]byte
	copy(localIP[:], parsedLocalIP)

	ep := &RawSocketEndpoint{
		sendFd:  sendFd,
		recvFd:  recvFd,
		nicID:   nicID,
		localIP: localIP,
		nat:     newRawNATTable(),
	}

	go ep.readLoop()
	return ep, nil
}

func (e *RawSocketEndpoint) SetTransportSender(sendFunc func([]byte)) {
	e.sendToTransport = sendFunc
}

func openRawTCPReceiveSocket(nicID tcpip.NICID) (int, error) {
	recvFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_TCP)
	if err != nil {
		return -1, fmt.Errorf("recv socket failed: %v (need root)", err)
	}
	if err := syscall.SetsockoptInt(recvFd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 16*1024*1024); err != nil {
		utils.Debugf("[RAW-NIC%d] SO_RCVBUF tuning failed: %v", nicID, err)
	}

	addr := &syscall.SockaddrInet4{Addr: [4]byte{0, 0, 0, 0}, Port: 0}
	if err := syscall.Bind(recvFd, addr); err != nil {
		syscall.Close(recvFd)
		return -1, fmt.Errorf("bind failed: %v", err)
	}
	return recvFd, nil
}

func (e *RawSocketEndpoint) currentRecvFD() (int, bool) {
	e.recvMu.Lock()
	defer e.recvMu.Unlock()
	return e.recvFd, !e.closed && e.recvFd >= 0
}

func (e *RawSocketEndpoint) isClosed() bool {
	e.recvMu.Lock()
	defer e.recvMu.Unlock()
	return e.closed
}

func (e *RawSocketEndpoint) reopenRecvFD(failedFD int) bool {
	newFD, err := openRawTCPReceiveSocket(e.nicID)
	if err != nil {
		count := e.readErrors.Load()
		e.logRateLimited(&e.lastReadErrLog,
			"[RAW-NIC%d] receive socket reopen failed after read error count=%d: %v",
			e.nicID, count, err)
		return false
	}

	e.recvMu.Lock()
	if e.closed {
		e.recvMu.Unlock()
		syscall.Close(newFD)
		return false
	}
	if e.recvFd != failedFD {
		e.recvMu.Unlock()
		syscall.Close(newFD)
		return true
	}
	e.recvFd = newFD
	e.recvMu.Unlock()

	_ = syscall.Close(failedFD)
	log.Printf("[RAW-NIC%d] receive socket reopened", e.nicID)
	return true
}

func (e *RawSocketEndpoint) logRateLimited(last *atomic.Int64, format string, args ...interface{}) {
	now := time.Now().UnixNano()
	for {
		previous := last.Load()
		if previous != 0 && time.Duration(now-previous) < rawSocketErrorLogInterval {
			return
		}
		if last.CompareAndSwap(previous, now) {
			log.Printf(format, args...)
			return
		}
	}
}

func (e *RawSocketEndpoint) readLoop() {
	buf := make([]byte, 65535)
	reopenBackoff := 100 * time.Millisecond

	for {
		recvFD, ok := e.currentRecvFD()
		if !ok {
			return
		}

		n, _, err := syscall.Recvfrom(recvFD, buf, 0)
		if err != nil {
			if e.isClosed() {
				return
			}
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				time.Sleep(10 * time.Millisecond)
				continue
			}

			count := e.readErrors.Add(1)
			e.logRateLimited(&e.lastReadErrLog,
				"[RAW-NIC%d] receive error count=%d: %v; reopening raw receive socket",
				e.nicID, count, err)
			if e.reopenRecvFD(recvFD) {
				reopenBackoff = 100 * time.Millisecond
				continue
			}

			time.Sleep(reopenBackoff)
			if reopenBackoff < 5*time.Second {
				reopenBackoff *= 2
				if reopenBackoff > 5*time.Second {
					reopenBackoff = 5 * time.Second
				}
			}
			continue
		}
		reopenBackoff = 100 * time.Millisecond
		if n < 40 || buf[0]>>4 != 4 {
			continue
		}
		ipHeaderLen := int(buf[0]&0x0f) * 4
		if ipHeaderLen < 20 || n < ipHeaderLen+20 || buf[9] != 6 {
			continue
		}

		tcpHeader := buf[ipHeaderLen:n]
		dstIP := [4]byte{buf[16], buf[17], buf[18], buf[19]}
		if dstIP != e.localIP {
			continue
		}

		remoteIP := [4]byte{buf[12], buf[13], buf[14], buf[15]}
		remotePort := uint16(tcpHeader[0])<<8 | uint16(tcpHeader[1])
		egressPort := uint16(tcpHeader[2])<<8 | uint16(tcpHeader[3])
		flow, ok := e.nat.inbound(egressPort, remoteIP, remotePort, time.Now())
		if !ok {
			continue
		}

		pktCopy := make([]byte, n)
		copy(pktCopy, buf[:n])
		copy(pktCopy[16:20], flow.clientIP[:])

		pktCopy[10], pktCopy[11] = 0, 0
		ipChecksumVal := network.IPChecksum(pktCopy[:ipHeaderLen])
		pktCopy[10] = byte(ipChecksumVal >> 8)
		pktCopy[11] = byte(ipChecksumVal)

		tcpCopy := pktCopy[ipHeaderLen:]
		tcpCopy[2] = byte(flow.clientPort >> 8)
		tcpCopy[3] = byte(flow.clientPort)
		tcpCopy[16], tcpCopy[17] = 0, 0
		srcIPBytes := [4]byte{pktCopy[12], pktCopy[13], pktCopy[14], pktCopy[15]}
		dstIPBytes := [4]byte{pktCopy[16], pktCopy[17], pktCopy[18], pktCopy[19]}
		tcpChecksumVal := network.TCPChecksum(tcpCopy, srcIPBytes, dstIPBytes)
		tcpCopy[16] = byte(tcpChecksumVal >> 8)
		tcpCopy[17] = byte(tcpChecksumVal)

		if e.sendToTransport != nil {
			e.sendToTransport(pktCopy)
		}
		e.packetIn.Add(1)
	}
}

func (e *RawSocketEndpoint) WritePackets(pkts stack.PacketBufferList) (int, tcpip.Error) {
	n := 0
	for _, pkt := range pkts.AsSlice() {
		ipPacket := pkt.ToView().ToSlice()
		if len(ipPacket) < 40 || ipPacket[0]>>4 != 4 {
			continue
		}

		pktCopy := make([]byte, len(ipPacket))
		copy(pktCopy, ipPacket)
		ipHeaderLen := int(pktCopy[0]&0x0f) * 4
		if ipHeaderLen < 20 || len(pktCopy) < ipHeaderLen+20 || pktCopy[9] != 6 {
			continue
		}
		tcpHeader := pktCopy[ipHeaderLen:]

		clientIP := [4]byte{pktCopy[12], pktCopy[13], pktCopy[14], pktCopy[15]}
		remoteIP := [4]byte{pktCopy[16], pktCopy[17], pktCopy[18], pktCopy[19]}
		clientPort := uint16(tcpHeader[0])<<8 | uint16(tcpHeader[1])
		remotePort := uint16(tcpHeader[2])<<8 | uint16(tcpHeader[3])
		egressPort, ok := e.nat.outbound(rawFlowKey{
			clientIP: clientIP, clientPort: clientPort,
			remoteIP: remoteIP, remotePort: remotePort,
		}, time.Now())
		if !ok {
			count := e.natFullEvents.Add(1)
			e.logRateLimited(&e.lastNATFullLog,
				"[RAW-NIC%d] source-port NAT table full events=%d active=%d packet-in=%d packet-out=%d",
				e.nicID, count, e.nat.size(), e.packetIn.Load(), e.packetOut.Load())
			continue
		}

		copy(pktCopy[12:16], e.localIP[:])
		tcpHeader[0] = byte(egressPort >> 8)
		tcpHeader[1] = byte(egressPort)

		pktCopy[10], pktCopy[11] = 0, 0
		ipChecksumVal := network.IPChecksum(pktCopy[:ipHeaderLen])
		pktCopy[10] = byte(ipChecksumVal >> 8)
		pktCopy[11] = byte(ipChecksumVal)

		tcpHeader[16], tcpHeader[17] = 0, 0
		srcIPBytes := [4]byte{pktCopy[12], pktCopy[13], pktCopy[14], pktCopy[15]}
		dstIPBytes := [4]byte{pktCopy[16], pktCopy[17], pktCopy[18], pktCopy[19]}
		tcpChecksumVal := network.TCPChecksum(tcpHeader, srcIPBytes, dstIPBytes)
		tcpHeader[16] = byte(tcpChecksumVal >> 8)
		tcpHeader[17] = byte(tcpChecksumVal)

		addr := &syscall.SockaddrInet4{Addr: remoteIP, Port: 0}
		if err := syscall.Sendto(e.sendFd, pktCopy, 0, addr); err != nil {
			count := e.sendErrors.Add(1)
			e.logRateLimited(&e.lastSendErrLog,
				"[RAW-NIC%d] Sendto failed count=%d: %v", e.nicID, count, err)
			continue
		}

		e.packetOut.Add(1)
		n++
	}
	return n, nil
}

func (e *RawSocketEndpoint) MTU() uint32                    { return 1500 }
func (e *RawSocketEndpoint) MaxHeaderLength() uint16        { return 0 }
func (e *RawSocketEndpoint) LinkAddress() tcpip.LinkAddress { return "" }
func (e *RawSocketEndpoint) Capabilities() stack.LinkEndpointCapabilities {
	return stack.CapabilityNone
}
func (e *RawSocketEndpoint) Attach(dispatcher stack.NetworkDispatcher) {
	e.dispatcher = dispatcher
}
func (e *RawSocketEndpoint) IsAttached() bool                        { return e.dispatcher != nil }
func (e *RawSocketEndpoint) Wait()                                   {}
func (e *RawSocketEndpoint) ARPHardwareType() header.ARPHardwareType { return header.ARPHardwareNone }
func (e *RawSocketEndpoint) AddHeader(*stack.PacketBuffer)           {}
func (e *RawSocketEndpoint) Close() {
	e.recvMu.Lock()
	if e.closed {
		e.recvMu.Unlock()
		return
	}
	e.closed = true
	recvFD := e.recvFd
	e.recvFd = -1
	e.recvMu.Unlock()

	_ = syscall.Close(e.sendFd)
	if recvFD >= 0 {
		_ = syscall.Close(recvFD)
	}
}
func (e *RawSocketEndpoint) SetMTU(uint32)                        {}
func (e *RawSocketEndpoint) SetLinkAddress(tcpip.LinkAddress)     {}
func (e *RawSocketEndpoint) ParseHeader(*stack.PacketBuffer) bool { return true }
func (e *RawSocketEndpoint) SetOnCloseAction(func())              {}
