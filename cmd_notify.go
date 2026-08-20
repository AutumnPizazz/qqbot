package main

// notify 客户端子命令：AI agent / 脚本通过 HTTPS 调用云服务器 qqbot 的
// /api/v1/notify 端点，让机器人向管理员 QQ（或群）发送消息。
//
//   qqbot notify send --server https://qqbot.civgo.top:8443 --token <t> --to 2170191481 "进度：构建完成"
//   echo "进度：测试通过" | qqbot notify send --server ... --token ... --to 2170191481
//   qqbot notify send --server ... --token ... --group 675179266 --text "群消息"
//
// 消息来源优先级：--text > 位置参数 > stdin（非终端时）。
// 退出码：0=成功；1=请求失败/发送失败；2=参数错误。

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const notifyClientVersion = "1.0"

type notifyClient struct {
	server string // 例如 https://qqbot.civgo.top:8443
	token  string
	http   *http.Client
}

// do 发送一次 notify 请求，返回 (http状态码, 响应体, error)。
func (c *notifyClient) do(method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.server+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, err
}

// runNotifyCmd 处理 notify 子命令。
func runNotifyCmd(args []string) int {
	if len(args) == 0 {
		printNotifyUsage()
		return 2
	}
	switch args[0] {
	case "send", "s":
		return runNotifySend(args[1:])
	case "help", "-h", "--help":
		printNotifyUsage()
		return 0
	default:
		fmt.Fprintln(os.Stderr, "未知 notify 子命令: "+args[0])
		printNotifyUsage()
		return 2
	}
}

func printNotifyUsage() {
	fmt.Print(`qqbot notify — 通过 QQ 消息向管理员传信（AI agent 进度通报）

用法:
  qqbot notify send [选项] [消息...]
      向指定 QQ（私聊）或群（群聊）发送一条消息。

选项:
  --server <url>   qqbot 通知 API 地址（默认 https://qqbot.civgo.top:30443，
                   或环境变量 QQBOT_NOTIFY_SERVER）
  --token <t>      通知 token（默认环境变量 QQBOT_NOTIFY_TOKEN）
  --to <qq>        私聊目标 QQ（与 --group 二选一）
  --group <gid>    群聊目标群号（与 --to 二选一）
  --text <msg>     消息内容（也可用位置参数，或从 stdin 管道读取）
  --timeout <sec>  请求超时秒数（默认 15）
  -h, --help       帮助

消息来源优先级: --text > 位置参数 > stdin（非终端时自动读取）

示例:
  qqbot notify send --token xxx --to 2170191481 "进度：构建完成"
  echo "进度：全部测试通过" | qqbot notify send --token xxx --to 2170191481
  qqbot notify send --token xxx --group 675179266 --text "发布完成，请查收"

环境变量: QQBOT_NOTIFY_SERVER / QQBOT_NOTIFY_TOKEN 可省去重复传参。
`)
}

func runNotifySend(args []string) int {
	fs := flag.NewFlagSet("notify send", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", envOr("QQBOT_NOTIFY_SERVER", "https://qqbot.civgo.top:30443"), "通知 API 地址")
	token := fs.String("token", os.Getenv("QQBOT_NOTIFY_TOKEN"), "通知 token")
	to := fs.Int64("to", 0, "私聊目标 QQ")
	group := fs.Int64("group", 0, "群聊目标群号")
	text := fs.String("text", "", "消息内容")
	timeoutSec := fs.Int("timeout", 15, "请求超时秒数")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *token == "" {
		fmt.Fprintln(os.Stderr, "错误: 缺少 --token（或设置环境变量 QQBOT_NOTIFY_TOKEN）")
		return 2
	}
	if *to == 0 && *group == 0 {
		fmt.Fprintln(os.Stderr, "错误: 必须指定 --to（私聊 QQ）或 --group（群号）之一")
		return 2
	}
	if *to != 0 && *group != 0 {
		fmt.Fprintln(os.Stderr, "错误: --to 与 --group 只能指定其一")
		return 2
	}

	// 消息内容：--text > 位置参数 > stdin
	msg := *text
	rest := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if msg == "" && rest != "" {
		msg = rest
	}
	if msg == "" {
		stdin, err := readStdin()
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取 stdin 失败:", err)
			return 1
		}
		msg = strings.TrimSpace(stdin)
	}
	if msg == "" {
		fmt.Fprintln(os.Stderr, "错误: 消息内容为空（用 --text / 位置参数 / stdin 提供）")
		return 2
	}
	if len([]rune(msg)) > 5000 {
		fmt.Fprintln(os.Stderr, "错误: 消息过长（最多 5000 字符）")
		return 2
	}

	c := &notifyClient{
		server: strings.TrimRight(*server, "/"),
		token:  *token,
		http:   &http.Client{Timeout: time.Duration(*timeoutSec) * time.Second},
	}

	payload := map[string]any{"text": msg}
	if *to != 0 {
		payload["to"] = *to
	} else {
		payload["group_id"] = *group
	}
	status, body, err := c.do("POST", "/api/v1/notify", payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, "请求失败:", err)
		return 1
	}
	// 尽力解析响应体用于友好提示
	var resp struct {
		Ok        bool   `json:"ok"`
		Action    string `json:"action"`
		MessageID int64  `json:"message_id"`
		Error     struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &resp)
	switch {
	case status == http.StatusOK && resp.Ok:
		fmt.Printf("✅ 已发送（%s message_id=%d）\n", resp.Action, resp.MessageID)
		return 0
	case status == http.StatusUnauthorized:
		fmt.Fprintln(os.Stderr, "❌ 通知 token 无效（401）")
		return 1
	case status == http.StatusServiceUnavailable:
		fmt.Fprintf(os.Stderr, "❌ 服务不可用（503）: %s\n", orStr(resp.Error.Message, "机器人离线或 notify 未启用"))
		return 1
	default:
		fmt.Fprintf(os.Stderr, "❌ 发送失败（HTTP %d）: %s\n", status, orStr(resp.Error.Message, string(body)))
		return 1
	}
}

// readStdin 读取标准输入全部内容。
func readStdin() (string, error) {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
