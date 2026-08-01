BINARY  := qqbot
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build build-linux build-win run docker-build clean

# 本机构建（Windows / Linux 通用）
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

# 交叉编译 Linux amd64（部署到服务器）
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .

# 交叉编译 Windows amd64（本机运行）
build-win:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-windows-amd64.exe .

run:
	go run . --data-dir data --master-key-file $$QQBOT_MASTER_KEY_FILE

# 构建 Docker 镜像（amd64）
docker-build:
	docker build -t qqbot:$(VERSION) .

clean:
	rm -rf bin
