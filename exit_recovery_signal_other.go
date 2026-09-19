//go:build !linux

package main

import "universal-bypass-tool/transport"

func installExitRecoverySignal(transport.Transport) {}
