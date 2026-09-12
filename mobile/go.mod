module openflux-mobile

go 1.26.4

require universal-bypass-tool v0.0.0

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.27 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/mobile v0.0.0-20260908204917-8b95e45f8d3e // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/tools v0.50.0 // indirect
)

replace universal-bypass-tool => ..

tool golang.org/x/mobile/cmd/gobind
