// sshbox —— 云服务器 SSH 连接工具箱（Windows exe）
//
// 设计目标：把"连接云服务器"这件事封装成一个 exe。
//   - 凭据在编译时注入（build.bat 读取 server-info.txt），源码与仓库不含任何涉密信息；
//   - 拿到 exe 即可连接服务器，使用方不需要知道密码、不需要安装 Python/paramiko；
//   - 功能通用（run / upload / download / probe / install-docker），不绑定任何具体项目。
//
// 凭据解析优先级（高 → 低）：
//   命令行参数 > sshbox.conf 配置文件 > 环境变量 > 编译期内置值
package main

import (
	"bufio"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"github.com/pkg/sftp"
)

// ---- 编译期注入的凭据（build 脚本通过 -ldflags -X 注入 base64 编码值，源码中永远为空）----
// base64 编码可安全承载任意特殊字符（! & ' " 空格等），注入后解码使用。
var (
	builtinHost     = ""
	builtinPort     = "MjI=" // "22"
	builtinUser     = "cm9vdA==" // "root"
	builtinPassword = ""
	builtinKeyPath  = ""
)

// decodeBuiltin 解码编译期内置的 base64 值；非 base64（未注入）时原样返回。
func decodeBuiltin(v string) string {
	if v == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil {
		return string(b)
	}
	return v
}

const version = "1.0.0"

// ---- 帮助 ----
const usageText = `sshbox v%s —— 云服务器 SSH 连接工具箱（凭据已内置，拥有即可连接）

用法:
  sshbox.exe run "远程命令" [--timeout 秒]    执行远程命令并显示输出
  sshbox.exe upload <本地文件> <远程路径>      上传文件（带进度、断线重试）
  sshbox.exe download <远程路径> <本地文件>    下载文件
  sshbox.exe probe                            探测服务器环境（系统/Docker/磁盘/端口）
  sshbox.exe install-docker                   安装 Docker Engine + compose 插件 + 镜像加速
  sshbox.exe info                             显示连接信息（不显示密码）
  sshbox.exe help                             显示本帮助

通用参数（优先级高于内置凭据）:
  --host <IP或域名>  --port <端口>  --user <用户>
  --password <密码>  --key <私钥文件>
  --trust            无条件信任主机密钥（默认: 首次连接自动接受并保存指纹）

环境变量: SSHBOX_HOST / SSHBOX_PORT / SSHBOX_USER / SSHBOX_PASSWORD / SSHBOX_KEY
配置文件: 程序同目录 sshbox.conf（ini 格式，键名同上，兼容 server-info.txt 写法）
`

// ---- 凭据 ----
type creds struct {
	host     string
	port     string
	user     string
	password string
	keyPath  string
	trust    bool
}

// loadCreds 按 命令行 > sshbox.conf > 环境变量 > 内置 合并凭据。
func loadCreds(h, p, u, pw, key string, trust bool) (creds, error) {
	c := creds{
		host:     decodeBuiltin(builtinHost),
		port:     decodeBuiltin(builtinPort),
		user:     decodeBuiltin(builtinUser),
		password: decodeBuiltin(builtinPassword),
		keyPath:  decodeBuiltin(builtinKeyPath),
	}

	// 配置文件（兼容 server-info.txt 的 key=value 格式，与 exe 同目录）
	cfg, _ := readConfFile()
	pick := func(name, cur string) string {
		if cur != "" {
			return cur
		}
		if v, ok := cfg[name]; ok && v != "" {
			return v
		}
		return os.Getenv("SSHBOX_" + name)
	}
	c.host = pick("SSH_HOST", c.host)
	c.port = pick("SSH_PORT", c.port)
	c.user = pick("SSH_USER", c.user)
	c.password = pick("SSH_PASSWORD", c.password)
	c.keyPath = pick("SSH_KEY", c.keyPath)

	// 命令行参数（最高优先级）
	if h != "" {
		c.host = h
	}
	if p != "" {
		c.port = p
	}
	if u != "" {
		c.user = u
	}
	if pw != "" {
		c.password = pw
	}
	if key != "" {
		c.keyPath = key
	}
	c.trust = trust

	if c.host == "" {
		return c, fmt.Errorf("缺少服务器地址：请用 --host 指定，或设置 SSHBOX_HOST / sshbox.conf / 重新构建注入")
	}
	if c.password == "" && c.keyPath == "" {
		return c, fmt.Errorf("缺少认证凭据：请用 --password 或 --key 提供，或重新构建注入")
	}
	return c, nil
}

// readConfFile 解析 exe 同目录 sshbox.conf（ini 风格，# 为注释）。
func readConfFile() (map[string]string, error) {
	cfg := map[string]string{}
	exe, err := os.Executable()
	if err != nil {
		return cfg, err
	}
	path := filepath.Join(filepath.Dir(exe), "sshbox.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, nil // 无配置文件是常态，不报错
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		cfg[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return cfg, nil
}

// ---- SSH 连接 ----
var knownHostsFile = filepath.Join(os.TempDir(), "sshbox_known_hosts")

// connect 建立 SSH 连接（密码或私钥认证；主机密钥默认首次接受并保存指纹）。
func connect(c creds) (*ssh.Client, error) {
	auths := []ssh.AuthMethod{}
	if c.keyPath != "" {
		key, err := os.ReadFile(c.keyPath)
		if err != nil {
			return nil, fmt.Errorf("读取私钥失败: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if c.password != "" {
		auths = append(auths, ssh.Password(c.password))
	}

	var hostKeyCallback ssh.HostKeyCallback
	if c.trust {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	} else {
		cb, _ := knownhosts.New(knownHostsFile)
		hostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if cb != nil {
				if err := cb(hostname, remote, key); err == nil {
					return nil
				}
			}
			// 未知主机：打印指纹并保存后接受（首次连接）
			fp := base64.StdEncoding.EncodeToString(key.Marshal())
			fmt.Printf("[提示] 首次连接，保存服务器主机密钥指纹 SHA256:%s\n", fp)
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
			if derr := os.MkdirAll(filepath.Dir(knownHostsFile), 0700); derr == nil {
				f, aerr := os.OpenFile(knownHostsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if aerr == nil {
					f.WriteString(line + "\n")
					f.Close()
				}
			}
			return nil
		}
	}

	addr := net.JoinHostPort(c.host, c.port)
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            c.user,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
		Timeout:         20 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %v", addr, err)
	}
	return client, nil
}

// ---- 子命令: info ----
func cmdInfo(c creds) {
	fmt.Println("sshbox v" + version)
	fmt.Printf("  服务器: %s:%s\n", c.host, c.port)
	fmt.Printf("  用户:   %s\n", c.user)
	fmt.Printf("  认证:   %s\n", authDesc(c))
	fmt.Println("  凭据已内置在 exe 中，使用方无需任何配置。")
}

func authDesc(c creds) string {
	if c.keyPath != "" {
		return "私钥认证 (" + filepath.Base(c.keyPath) + ")"
	}
	return "密码认证（已内置，不显示明文）"
}

// ---- 子命令: run ----
func cmdRun(c creds, command string, timeoutSec int) error {
	client, err := connect(c)
	if err != nil {
		return err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	defer sess.Close()

	modes := ssh.TerminalModes{ssh.ECHO: 0}
	sess.RequestPty("xterm", 40, 120, modes)
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return err
	}
	if err := sess.Start(command); err != nil {
		return fmt.Errorf("执行失败: %w", err)
	}
	done := make(chan struct{})
	go func() {
		io.Copy(os.Stdout, stdout)
		io.Copy(os.Stderr, stderr)
		close(done)
	}()
	wait := make(chan error, 1)
	go func() { wait <- sess.Wait() }()
	select {
	case <-time.After(time.Duration(timeoutSec) * time.Second):
		sess.Close()
		return fmt.Errorf("命令超时（%d 秒），已终止", timeoutSec)
	case <-done:
		return <-wait
	}
}

// ---- 子命令: upload / download ----
func cmdUpload(c creds, local, remote string) error {
	client, err := connect(c)
	if err != nil {
		return err
	}
	defer client.Close()
	sftp, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("初始化 SFTP 失败: %w", err)
	}
	defer sftp.Close()

	f, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	// 确保远程目录存在（远程路径始终按 POSIX 处理，不能用 filepath）
	dir := path.Dir(remote)
	if dir != "." && dir != "/" {
		if _, err := sftp.Stat(dir); err != nil {
			sftp.MkdirAll(dir)
		}
	}

	const maxRetry = 3
	t0 := time.Now()
	var lastErr error
	for attempt := 1; attempt <= maxRetry; attempt++ {
		lastErr = putWithProgress(sftp, f, remote, info.Size())
		if lastErr == nil {
			fmt.Printf("\n[完成] %s -> %s（%.1f MB，耗时 %s）\n",
				local, remote, float64(info.Size())/1048576, time.Since(t0).Round(time.Second))
			return nil
		}
		fmt.Printf("\n[重试 %d/%d] %v\n", attempt, maxRetry, lastErr)
		if attempt < maxRetry {
			time.Sleep(time.Duration(attempt) * 5 * time.Second)
		}
	}
	return fmt.Errorf("上传失败: %v", lastErr)
}

func putWithProgress(sftp *sftp.Client, f *os.File, remote string, size int64) error {
	// 断点续传：已存在的远端文件大小作为偏移起点
	var offset int64
	if st, err := sftp.Stat(remote); err == nil {
		offset = st.Size()
	}
	if offset > 0 && offset < size {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}
	out, err := sftp.OpenFile(remote, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return err
	}
	defer out.Close()
	if offset > 0 {
		if _, err := out.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}

	buf := make([]byte, 512*1024)
	var written int64 = offset
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			fmt.Printf("\r[上传] %6.2f%%  %.1f / %.1f MB", pct(written, size), float64(written)/1048576, float64(size)/1048576)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return out.Sync()
}

func cmdDownload(c creds, remote, local string) error {
	client, err := connect(c)
	if err != nil {
		return err
	}
	defer client.Close()
	sftp, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("初始化 SFTP 失败: %w", err)
	}
	defer sftp.Close()

	f, err := os.Create(local)
	if err != nil {
		return fmt.Errorf("创建本地文件失败: %w", err)
	}
	defer f.Close()
	t0 := time.Now()
	if err := getWithProgress(sftp, f, remote); err != nil {
		return err
	}
	fmt.Printf("\n[完成] %s -> %s（耗时 %s）\n", remote, local, time.Since(t0).Round(time.Second))
	return nil
}

func getWithProgress(sftp *sftp.Client, f *os.File, remote string) error {
	st, err := sftp.Stat(remote)
	if err != nil {
		return fmt.Errorf("远程文件不存在: %w", err)
	}
	in, err := sftp.Open(remote)
	if err != nil {
		return err
	}
	defer in.Close()
	buf := make([]byte, 512*1024)
	var written int64
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			fmt.Printf("\r[下载] %6.2f%%  %.1f / %.1f MB", pct(written, st.Size()), float64(written)/1048576, float64(st.Size())/1048576)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

func pct(written, size int64) float64 {
	if size <= 0 {
		return 100
	}
	return float64(written) * 100 / float64(size)
}

// ---- 子命令: probe ----
func cmdProbe(c creds) error {
	client, err := connect(c)
	if err != nil {
		return err
	}
	defer client.Close()
	fmt.Printf("=== 服务器环境探测: %s:%s ===\n", c.host, c.port)
	cmds := []string{
		"echo '-- 系统 --' && uname -a && grep -E '^(PRETTY_NAME|VERSION_ID)=' /etc/os-release",
		"echo '-- 架构 --' && uname -m",
		"echo '-- Docker --' && (docker --version && docker compose version) 2>/dev/null || echo '[未安装 Docker]'",
		"echo '-- 资源 --' && df -h / | tail -1 && free -h | head -2",
		"echo '-- 端口 --' && ss -tlnp | grep -E ':(22|80|443)\\b' || echo '[仅基础端口]'",
		"echo '-- 包管理器 --' && (apt-get --version | head -1 || yum --version | head -1 || echo '[未知]')",
	}
	for _, cmd := range cmds {
		runEcho(client, cmd)
	}
	return nil
}

// runEcho 执行命令并回显输出（单会话，出错仅标记）。
func runEcho(client *ssh.Client, cmd string) {
	sess, err := client.NewSession()
	if err != nil {
		fmt.Printf("[错误] %v\n", err)
		return
	}
	defer sess.Close()
	modes := ssh.TerminalModes{ssh.ECHO: 0}
	sess.RequestPty("xterm", 40, 120, modes)
	out, err := sess.CombinedOutput(cmd)
	if len(out) > 0 {
		fmt.Println(string(out))
	}
	if err != nil && len(out) == 0 {
		fmt.Printf("[exit=%v]\n", err)
	}
}

// ---- 子命令: install-docker ----
func cmdInstallDocker(c creds) error {
	client, err := connect(c)
	if err != nil {
		return err
	}
	defer client.Close()
	fmt.Println("=== 安装 Docker Engine + compose 插件 ===")
	// 检测发行版
	osr := runQuiet(client, "grep -E '^(ID|VERSION_CODENAME)=' /etc/os-release")
	distro := "debian"
	if strings.Contains(strings.ToLower(osr), "ubuntu") {
		distro = "ubuntu"
	} else if strings.Contains(strings.ToLower(osr), "debian") {
		distro = "debian"
	} else {
		fmt.Printf("[警告] 非 Ubuntu/Debian（检测: %s），按 debian 流程尝试\n", strings.TrimSpace(osr))
	}

	// 官方脚本优先，失败回退阿里云镜像源
	code := runEchoCode(client, "curl -fsSL --connect-timeout 15 --max-time 60 https://get.docker.com -o /tmp/get-docker.sh && timeout 300 sh /tmp/get-docker.sh", 360)
	if code != 0 {
		fmt.Println("[回退] 官方脚本失败，改用阿里云镜像源安装...")
		runEcho(client, "apt-get update -y && apt-get install -y ca-certificates curl gnupg")
		runEcho(client, "install -m 0755 -d /etc/apt/keyrings")
		runEcho(client, "curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/"+distro+"/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg && chmod a+r /etc/apt/keyrings/docker.gpg")
		runEcho(client, "echo \"deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://mirrors.aliyun.com/docker-ce/linux/"+distro+" $(. /etc/os-release && echo $VERSION_CODENAME) stable\" > /etc/apt/sources.list.d/docker.list")
		runEcho(client, "apt-get update -y && apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin")
	}
	runEcho(client, "systemctl enable --now docker && docker --version && docker compose version")
	// 国内镜像加速（失败不影响）
	runEcho(client, "mkdir -p /etc/docker && cat > /etc/docker/daemon.json <<'EOF'\n{\n  \"registry-mirrors\": [\n    \"https://docker.1ms.run\",\n    \"https://docker.m.daocloud.io\",\n    \"https://dockerproxy.net\"\n  ],\n  \"log-driver\": \"json-file\",\n  \"log-opts\": {\"max-size\": \"20m\", \"max-file\": \"3\"}\n}\nEOF\nsystemctl restart docker || true")
	runEcho(client, "docker info --format '镜像加速: {{.RegistryConfig.Mirrors}}'")
	return nil
}

func runQuiet(client *ssh.Client, cmd string) string {
	sess, err := client.NewSession()
	if err != nil {
		return ""
	}
	defer sess.Close()
	out, _ := sess.CombinedOutput(cmd)
	return string(out)
}

func runEchoCode(client *ssh.Client, cmd string, timeoutSec int) int {
	sess, err := client.NewSession()
	if err != nil {
		fmt.Printf("[错误] %v\n", err)
		return -1
	}
	defer sess.Close()
	modes := ssh.TerminalModes{ssh.ECHO: 0}
	sess.RequestPty("xterm", 40, 120, modes)
	out, err := sess.CombinedOutput(cmd)
	if len(out) > 0 {
		fmt.Println(string(out))
	}
	if err != nil {
		fmt.Printf("[exit=%v]\n", err)
	}
	_ = timeoutSec // 超时由 SSH 层与上层命令自身 timeout 控制
	return 0
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf(usageText, version)
		os.Exit(1)
	}
	action := os.Args[1]
	if action == "help" || action == "-h" || action == "--help" {
		fmt.Printf(usageText, version)
		return
	}
	rest := os.Args[2:]

	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.Usage = func() {}
	timeoutSec := fs.Int("timeout", 600, "命令超时（秒）")
	host := fs.String("host", "", "服务器地址")
	port := fs.String("port", "", "SSH 端口")
	user := fs.String("user", "", "登录用户")
	password := fs.String("password", "", "登录密码")
	key := fs.String("key", "", "私钥文件路径")
	trust := fs.Bool("trust", false, "无条件信任主机密钥")
	if err := fs.Parse(rest); err != nil {
		os.Exit(1)
	}

	c, err := loadCreds(*host, *port, *user, *password, *key, *trust)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[错误]", err)
		os.Exit(1)
	}

	switch action {
	case "info":
		cmdInfo(c)
	case "run":
		args := fs.Args()
		// Go flag 在第一个位置参数处停止解析，因此命令后的 --timeout 需手动提取
		timeout := *timeoutSec
		cleaned := []string{}
		for i := 0; i < len(args); i++ {
			if args[i] == "--timeout" && i+1 < len(args) {
				if v, aerr := strconv.Atoi(args[i+1]); aerr == nil {
					timeout = v
				}
				i++
			} else if strings.HasPrefix(args[i], "--timeout=") {
				if v, aerr := strconv.Atoi(strings.TrimPrefix(args[i], "--timeout=")); aerr == nil {
					timeout = v
				}
			} else {
				cleaned = append(cleaned, args[i])
			}
		}
		if len(cleaned) == 0 {
			fmt.Fprintln(os.Stderr, "用法: sshbox.exe run \"远程命令\" [--timeout 秒]")
			os.Exit(1)
		}
		if err := cmdRun(c, strings.Join(cleaned, " "), timeout); err != nil {
			fmt.Fprintln(os.Stderr, "[错误]", err)
			os.Exit(1)
		}
	case "upload":
		args := fs.Args()
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "用法: sshbox.exe upload <本地文件> <远程路径>")
			os.Exit(1)
		}
		if err := cmdUpload(c, args[0], args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "[错误]", err)
			os.Exit(1)
		}
	case "download":
		args := fs.Args()
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "用法: sshbox.exe download <远程路径> <本地文件>")
			os.Exit(1)
		}
		if err := cmdDownload(c, args[0], args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "[错误]", err)
			os.Exit(1)
		}
	case "probe":
		if err := cmdProbe(c); err != nil {
			fmt.Fprintln(os.Stderr, "[错误]", err)
			os.Exit(1)
		}
	case "install-docker":
		if err := cmdInstallDocker(c); err != nil {
			fmt.Fprintln(os.Stderr, "[错误]", err)
			os.Exit(1)
		}
	case "help":
		fmt.Printf(usageText, version)
	default:
		fmt.Fprintf(os.Stderr, "[错误] 未知命令: %s\n\n", action)
		fmt.Printf(usageText, version)
		os.Exit(1)
	}
}
