# 多阶段构建：静态编译，镜像内不含 Go 工具链
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# -tags timetzdata：把时区数据库嵌入二进制（程序用 time.LoadLocation 按群配置时区，
# 运行阶段是 scratch 没有 /usr/share/zoneinfo，必须嵌入）
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -tags timetzdata -ldflags "-s -w" -o /out/qqbot .

# 仅提取根证书（scratch 没有包管理器，alpine 也只当证书源用）
FROM alpine:3.21 AS certs
RUN apk add --no-cache ca-certificates

# 运行阶段：scratch。二进制无 CGO、无外部命令调用，纯静态可直接运行，
# 镜像内只有二进制 + 根证书 + 示例配置，无 shell/包管理器，攻击面最小。
FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
WORKDIR /app
COPY --from=builder /out/qqbot /app/qqbot
COPY config.example.yaml /app/config.example.yaml
# 网页化启动：数据目录挂载 /app/data，主密钥挂载 /app/keys/master.key（只读）
# 监听 0.0.0.0:8080（容器内），对外映射请限制在内网/VPN
ENTRYPOINT ["/app/qqbot", "--data-dir", "/app/data", "--master-key-file", "/app/keys/master.key", "--admin-listen", "0.0.0.0:8080"]
