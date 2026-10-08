GO ?= /usr/local/go/bin/go
UNIT = $(HOME)/.config/systemd/user/exe-easel.service
SYSTEMCTL = XDG_RUNTIME_DIR=/run/user/$(shell id -u) systemctl --user

.PHONY: build test install restart

build:
	$(GO) build -o exe-easel .

test:
	$(GO) test ./...

install: build
	install -m 644 exe-easel.service $(UNIT)
	$(SYSTEMCTL) daemon-reload
	$(SYSTEMCTL) enable --now exe-easel
	$(SYSTEMCTL) restart exe-easel

restart: build
	$(SYSTEMCTL) restart exe-easel
