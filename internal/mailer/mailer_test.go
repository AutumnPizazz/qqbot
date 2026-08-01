package mailer

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSMTPServer 最小 SMTP 服务端（接收并记录邮件，支持 STARTTLS 跳过）。
type fakeSMTPServer struct {
	ln     net.Listener
	port   int
	emails chan string // 收到的完整邮件原文（DATA 内容）
}

func startFakeSMTP(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTPServer{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, emails: make(chan string, 4)}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeSMTPServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTPServer) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	write := func(line string) { conn.Write([]byte(line + "\r\n")) }
	write("220 fake ESMTP")
	var data strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				s.emails <- data.String()
				data.Reset()
				write("250 OK")
			} else {
				data.WriteString(line + "\n")
			}
			continue
		}
		cmd := line
		if sp := strings.IndexByte(line, ' '); sp >= 0 {
			cmd = line[:sp]
		}
		switch strings.ToUpper(cmd) {
		case "EHLO", "HELO":
			write("250-fake")
			write("250 AUTH PLAIN LOGIN")
		case "AUTH":
			write("235 2.7.0 OK")
		case "MAIL", "RCPT":
			write("250 OK")
		case "DATA":
			write("354 go ahead")
			inData = true
		case "QUIT":
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}

func TestSMTPBuildMessage(t *testing.T) {
	msg, err := buildMessage("bot@example.com", "admin@example.com", "登录验证码", "你的验证码是 123456")
	if err != nil {
		t.Fatal(err)
	}
	text := string(msg)
	if !strings.Contains(text, "From: bot@example.com") || !strings.Contains(text, "To: admin@example.com") {
		t.Fatalf("地址头缺失: %s", text)
	}
	if !strings.Contains(text, "=?UTF-8?B?") {
		t.Fatalf("主题未编码: %s", text)
	}
	if !strings.Contains(text, "base64") {
		t.Fatal("缺少 base64 编码声明")
	}
	// 正文可解码
	bodyB64 := ""
	for _, ln := range strings.Split(text, "\r\n") {
		if ln != "" && !strings.Contains(ln, ":") {
			bodyB64 += ln
		}
	}
	dec, err := base64.StdEncoding.DecodeString(bodyB64)
	if err != nil || !strings.Contains(string(dec), "123456") {
		t.Fatalf("正文解码失败: %v %q", err, string(dec))
	}
}

func TestSMTPSendPlain(t *testing.T) {
	fake := startFakeSMTP(t)
	s := New(Config{Host: "127.0.0.1", Port: fake.port, User: "u", Password: "p", From: "bot@example.com"})
	if err := s.Send("admin@example.com", "验证码", "code=654321"); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	select {
	case raw := <-fake.emails:
		if !strings.Contains(raw, "admin@example.com") || !strings.Contains(raw, "=?UTF-8?B?") {
			t.Fatalf("邮件头错误: %q", raw)
		}
		// 正文 base64 解码后应包含明文
		bodyB64 := ""
		for _, ln := range strings.Split(raw, "\n") {
			if ln != "" && !strings.Contains(ln, ":") && !strings.HasPrefix(ln, "=") {
				bodyB64 += ln
			}
		}
		dec, err := base64.StdEncoding.DecodeString(bodyB64)
		if err != nil || !strings.Contains(string(dec), "code=654321") {
			t.Fatalf("正文解码失败: %v %q", err, string(dec))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到邮件")
	}
}

func TestSMTPMissingConfig(t *testing.T) {
	s := New(Config{})
	if err := s.Send("a@b.c", "s", "b"); err == nil {
		t.Fatal("空配置应报错")
	}
}

func TestSMTPConnectionRefused(t *testing.T) {
	// 未监听端口
	s := New(Config{Host: "127.0.0.1", Port: 1})
	if err := s.Send("a@b.c", "s", "b"); err == nil {
		t.Fatal("连接失败应报错")
	}
}
