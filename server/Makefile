GO ?= /usr/local/go/bin/go
UNIT = $(HOME)/.config/systemd/user/exe-art.service
SYSTEMCTL = XDG_RUNTIME_DIR=/run/user/$(shell id -u) systemctl --user

.PHONY: build test install restart

build:
	$(GO) build -o exe-art .

test:
	$(GO) test ./...

install: build
	install -m 644 exe-art.service $(UNIT)
	$(SYSTEMCTL) daemon-reload
	$(SYSTEMCTL) enable --now exe-art
	$(SYSTEMCTL) restart exe-art

restart: build
	$(SYSTEMCTL) restart exe-art
