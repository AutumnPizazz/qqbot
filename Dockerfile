# 多阶段构建：静态编译，镜像内不含 Go 工具链
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/qqbot .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/qqbot /app/qqbot
COPY config.example.yaml /app/config.example.yaml
# 网页化启动：数据目录挂载 /app/data，主密钥挂载 /app/keys/master.key（只读）
# 监听 0.0.0.0:8080（容器内），对外映射请限制在内网/VPN
ENTRYPOINT ["/app/qqbot", "--data-dir", "/app/data", "--master-key-file", "/app/keys/master.key", "--admin-listen", "0.0.0.0:8080"]
