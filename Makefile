# Build with custom IP and port, e.g.: make all IP=192.168.1.1 PORT=9000
IP   ?= localhost
PORT ?= 2026

# Default server URL for chore client; default listen address for chore_svr
SERVER_URL    = http://$(IP):$(PORT)
SERVER_LISTEN ?= :$(PORT)

.PHONY: all server client clean run open rerun
all: server client

client:
	go build -ldflags "-X main.defaultServerURL=$(SERVER_URL)" -o chore ./cmd/chore

server:
	go build -ldflags "-X main.defaultAddr=$(SERVER_LISTEN)" -o chore_svr ./cmd/chore_svr

clean:
	rm -f chore chore_svr

rerun:
	-pkill -9 chore_svr 2>/dev/null || true
	$(MAKE) run

run:
	nohup ./chore_svr >> chore_svr.log 2>&1 &
	@echo chore_svr started in background, log: chore_svr.log

open:
	./chore -o