PREFIX ?= $(HOME)/.local/bin

.PHONY: build test install uninstall

build:
	go build -o bin/arkd ./cmd/arkd
	go build -o bin/ark ./cmd/ark

test:
	go test -race ./...

install: build
	mkdir -p $(PREFIX)
	install -m 0755 bin/arkd bin/ark $(PREFIX)/
	$(PREFIX)/ark install

uninstall:
	-$(PREFIX)/ark uninstall
	rm -f $(PREFIX)/arkd $(PREFIX)/ark
