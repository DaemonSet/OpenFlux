//go:build linux

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"universal-bypass-tool/transport"
)

type exitSessionRecoverer interface {
	ForceRecover(reason string) bool
}

func installExitRecoverySignal(carrier transport.Transport) {
	recoverer, ok := carrier.(exitSessionRecoverer)
	if !ok {
		return
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)

	go func() {
		for range signals {
			if recoverer.ForceRecover("SIGUSR1") {
				log.Printf("[VOLGA] accepted SIGUSR1 full session recycle")
			} else {
				log.Printf("[VOLGA] ignored SIGUSR1 full session recycle")
			}
		}
	}()
}
