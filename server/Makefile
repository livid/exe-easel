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

.PHONY: build test install restart uninstall check engine

build:
	$(GO) build -o exe-easel .

test:
	$(GO) test ./...

# the preflight: everything Easel needs, said before anything is installed
check:
	@ok=1; \
	test -x "$(REPO)/engine/scripts/export_r16_studio" || { echo "missing: engine/ is empty: git submodule update --init"; ok=0; }; \
	test -n "$(NODE)" && "$(NODE)" -e 'const [a,b]=process.versions.node.split(".").map(Number); process.exit(a>23||(a==23&&b>=6)?0:1)' \
	  || { echo "missing: Node 23.6 or newer (node on PATH)"; ok=0; }; \
	test -x "$(CLAUDE)" || { echo "missing: Claude Code (claude on PATH, signed in)"; ok=0; }; \
	command -v cargo >/dev/null && cargo --version | awk '{split($$2,v,"."); exit !(v[1]>1 || v[2]>=84)}' \
	  || { echo "missing: Rust 1.84 or newer (rustup update): the engine's Cargo.toml uses resolver 3"; ok=0; }; \
	if ! command -v ffmpeg >/dev/null || ! command -v ffprobe >/dev/null; then \
	  echo "missing: FFmpeg with ffprobe and libx264, for the replays (brew install ffmpeg / apt install ffmpeg)"; ok=0; \
	elif ! enc=$$(ffmpeg -hide_banner -encoders 2>&1); then \
	  echo "broken: ffmpeg doesn't run: $$(echo "$$enc" | head -1) (brew reinstall ffmpeg)"; ok=0; \
	elif ! echo "$$enc" | grep -q libx264; then \
	  echo "missing: libx264 in FFmpeg, for the replays (an FFmpeg built with it: Homebrew's and Debian's are)"; ok=0; \
	fi; \
	test $$ok = 1 && echo "preflight: everything Easel needs is here"

# the engine's replay build, which finishing, replays and the export use
engine: check
	cd $(REPO)/engine && cargo build --release -p easel

ifeq ($(OS),Darwin)
install: engine build
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
install: engine build
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
