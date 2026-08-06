# 多阶段构建：静态编译，镜像内不含 Go 工具链
# 构建/运行统一用 glibc 系镜像（debian），避免 musl（alpine）的兼容性差异，
# 体积换取稳定性与容器内可调试性。
FROM golang:1.26 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# -tags timetzdata：把时区数据库嵌入二进制（debian-slim 默认不含 tzdata，
# 程序按群配置 time.LoadLocation，嵌入后不依赖系统时区文件）
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -tags timetzdata -ldflags "-s -w" -o /out/qqbot .

# 运行阶段：Debian slim。glibc 兼容性最好；自带 shell 可 docker exec 排查；
# 根证书一行 apt 安装，无需额外提取阶段。
FROM debian:trixie-slim
# 换国内镜像源（deb.debian.org 在国内常 502/超时）；海外构建可删除本行
RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/qqbot /app/qqbot
COPY config.example.yaml /app/config.example.yaml
# 网页化启动：数据目录挂载 /app/data，主密钥挂载 /app/keys/master.key（只读）
# 监听 0.0.0.0:8080（容器内），对外映射请限制在内网/VPN
ENTRYPOINT ["/app/qqbot", "--data-dir", "/app/data", "--master-key-file", "/app/keys/master.key", "--admin-listen", "0.0.0.0:8080"]
