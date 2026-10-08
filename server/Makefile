# exe-easel: build, test, and run as a service of the user: a systemd user
# unit on Linux, a launchd agent on macOS. The service names this checkout,
# node and claude as they are found when you install, so install again after
# moving any of them.
GO     ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
NODE   ?= $(shell command -v node 2>/dev/null)
CLAUDE ?= $(shell command -v claude 2>/dev/null || echo $(HOME)/.local/bin/claude)
REPO   := $(abspath $(CURDIR)/..)
BIN    := $(CURDIR)/exe-easel
OS     := $(shell uname -s)
# what the daemon and its painters need on PATH under a service manager
SVCPATH := $(dir $(NODE)):$(dir $(CLAUDE)):$(HOME)/.cargo/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin

UNIT      := $(HOME)/.config/systemd/user/exe-easel.service
SYSTEMCTL := XDG_RUNTIME_DIR=/run/user/$(shell id -u) systemctl --user
LABEL     := com.github.livid.exe-easel
PLIST     := $(HOME)/Library/LaunchAgents/$(LABEL).plist
FILL       = sed -e 's|@BIN@|$(BIN)|g' -e 's|@REPO@|$(REPO)|g' -e 's|@NODE@|$(NODE)|g' \
               -e 's|@CLAUDE@|$(CLAUDE)|g' -e 's|@PATH@|$(SVCPATH)|g' -e 's|@LABEL@|$(LABEL)|g'

.PHONY: build test install restart uninstall check

build:
	$(GO) build -o exe-easel .

test:
	$(GO) test ./...

check:
	@test -n "$(NODE)" || { echo "node not found on PATH (Node 24)"; exit 1; }
	@test -x "$(CLAUDE)" || { echo "claude not found: install Claude Code and sign in"; exit 1; }
	@test -x "$(REPO)/engine/scripts/export_r16_studio" || { echo "engine/ is empty: git submodule update --init"; exit 1; }

ifeq ($(OS),Darwin)
install: check build
	mkdir -p $(HOME)/Library/LaunchAgents $(REPO)/logs
	$(FILL) exe-easel.plist.in > $(PLIST)
	-launchctl bootout gui/$$(id -u)/$(LABEL) 2>/dev/null
	launchctl bootstrap gui/$$(id -u) $(PLIST)

restart: build
	launchctl kickstart -k gui/$$(id -u)/$(LABEL)

uninstall:
	-launchctl bootout gui/$$(id -u)/$(LABEL)
	rm -f $(PLIST)
else
install: check build
	mkdir -p $(dir $(UNIT))
	$(FILL) exe-easel.service.in > $(UNIT)
	$(SYSTEMCTL) daemon-reload
	$(SYSTEMCTL) enable --now exe-easel
	$(SYSTEMCTL) restart exe-easel

restart: build
	$(SYSTEMCTL) restart exe-easel

uninstall:
	-$(SYSTEMCTL) disable --now exe-easel
	rm -f $(UNIT)
	$(SYSTEMCTL) daemon-reload
endif
