# 编译时指定 IP 和端口，例如: make build IP=192.168.1.1 PORT=9000
IP   ?= localhost
PORT ?= 2026

# chore 客户端默认连接的服务器地址；chore_svr 默认监听地址
SERVER_URL    = http://$(IP):$(PORT)
SERVER_LISTEN ?= :$(PORT)

.PHONY: build
build:
	go build -ldflags "-X main.defaultServerURL=$(SERVER_URL)" -o chore ./cmd/chore
	go build -ldflags "-X main.defaultAddr=$(SERVER_LISTEN)" -o chore_svr ./cmd/chore_svr
