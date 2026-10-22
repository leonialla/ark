BIN := bin/ssh-tunnel-daemon
PREFIX ?= $(HOME)/.local/bin

.PHONY: build test install uninstall

build:
	go build -o $(BIN) ./cmd/ssh-tunnel-daemon

test:
	go test -race ./...

install: build
	mkdir -p $(PREFIX)
	install -m 0755 $(BIN) $(PREFIX)/ssh-tunnel-daemon
	$(PREFIX)/ssh-tunnel-daemon install

uninstall:
	-$(PREFIX)/ssh-tunnel-daemon uninstall
	rm -f $(PREFIX)/ssh-tunnel-daemon
