# sshbox —— 云服务器 SSH 连接工具箱

单文件 Windows exe，凭据编译时内置，**拿到即用**：无需密码、无需装 Python/paramiko。通用工具，任何项目/电脑都能用；源码与 git 仓库零涉密信息。

## 使用

```bat
sshbox.exe run "docker ps" --timeout 300      :: 远程命令（默认超时 600s）
sshbox.exe upload .\a.tar.gz /opt/a.tar.gz    :: 上传（进度条/断点续传/失败重试）
sshbox.exe download /opt/x.log .\x.log        :: 下载
sshbox.exe probe                              :: 探测系统/Docker/磁盘/端口
sshbox.exe install-docker                     :: 装 Docker + compose + 镜像加速
sshbox.exe info                               :: 显示连接信息（不显示密码）
```

## 重新构建（密码轮换/换服务器）

```bat
build.bat                     :: 自动读取 server-info.txt（或 SSHBOX_* 环境变量）注入凭据
build.bat -Conf D:\xx\server-info.txt
```

## 安全 ⚠️

- exe 内凭据可被 strings/反编译提取：**持有 exe ≈ 持有密码**，只分发给可信人员，勿传网盘/群文件
- 密码轮换后必须重新构建并重新分发，旧 exe 立即失效
- 临时连其他机器：`--host/--user/--password` 参数或同目录 `sshbox.conf` 可覆盖内置凭据
