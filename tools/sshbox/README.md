# sshbox —— 云服务器 SSH 连接工具箱

单文件 Linux 可执行文件，凭据编译时内置，**拿到即用**：无需密码、无需装 Python/paramiko。通用工具，任何 Linux 机器/服务器都能用；源码与 git 仓库零涉密信息。

## 使用

```bash
sshbox run "docker ps" --timeout 300      # 远程命令（默认超时 600s）
sshbox upload ./a.tar.gz /opt/a.tar.gz    # 上传（进度条/断点续传/失败重试）
sshbox download /opt/x.log ./x.log        # 下载
sshbox probe                              # 探测系统/Docker/磁盘/端口
sshbox install-docker                     # 装 Docker + compose + 镜像加速
sshbox info                               # 显示连接信息（不显示密码）
```

## 重新构建（密码轮换/换服务器）

```bash
./build-linux.sh                          # 自动读取 server-info.txt（或 SSHBOX_* 环境变量）注入凭据
./build-linux.sh /path/to/server-info.txt # 指定配置文件
GOARCH=arm64 ./build-linux.sh             # 换架构（默认 amd64）
```

产物默认输出到 `dist/sshbox-linux-amd64`。本地构建可交叉编译（`GOOS/GOARCH` 环境变量），无需在目标机器上装 Go。

> Windows 版请用 `build.bat`（产物 `sshbox.exe`）。

## 安全 ⚠️

- 二进制内凭据可被 strings/反编译提取：**持有可执行文件 ≈ 持有密码**，只分发给可信人员，勿传网盘/群文件
- 密码轮换后必须重新构建并重新分发，旧文件立即失效
- 临时连其他机器：`--host/--user/--password` 参数或同目录 `sshbox.conf` 可覆盖内置凭据
