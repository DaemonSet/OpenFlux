package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"

	"universal-bypass-tool/transport"
)

func startDNSTCPProxy(
	listenAddr string,
	mux *transport.DNSMuxTransport,
	primary string,
	fallback string,
) (net.Listener, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen DNS TCP %s: %w", listenAddr, err)
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go serveDNSTCPConn(
				conn,
				mux,
				primary,
				fallback,
			)
		}
	}()

	return listener, nil
}

func serveDNSTCPConn(
	conn net.Conn,
	mux *transport.DNSMuxTransport,
	primary string,
	fallback string,
) {
	defer conn.Close()

	for {
		var sizeBuf [2]byte

		if _, err := io.ReadFull(conn, sizeBuf[:]); err != nil {
			if !errors.Is(err, io.EOF) &&
				!errors.Is(err, io.ErrUnexpectedEOF) {
				log.Printf("DNS TCP read size: %v", err)
			}
			return
		}

		size := int(binary.BigEndian.Uint16(sizeBuf[:]))
		if size < 12 || size > 65535 {
			log.Printf("DNS TCP invalid query size: %d", size)
			return
		}

		query := make([]byte, size)

		if _, err := io.ReadFull(conn, query); err != nil {
			log.Printf("DNS TCP read query: %v", err)
			return
		}

		answer, err := mux.QueryDNS(query, primary)

		if err != nil && fallback != "" && fallback != primary {
			answer, err = mux.QueryDNS(query, fallback)
		}

		if err != nil {
			log.Printf("DNSMux query failed: %v", err)
			return
		}

		if len(answer) < 12 || len(answer) > 65535 {
			log.Printf("DNSMux invalid answer size: %d", len(answer))
			return
		}

		binary.BigEndian.PutUint16(
			sizeBuf[:],
			uint16(len(answer)),
		)

		if _, err := conn.Write(sizeBuf[:]); err != nil {
			return
		}

		if _, err := conn.Write(answer); err != nil {
			return
		}
	}
}
