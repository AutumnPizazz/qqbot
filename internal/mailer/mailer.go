// Package mailer 实现轻量 SMTP 发送（登录验证码邮件用）。
// 支持两种常见加密模式：
//   - 465：SSL/TLS 直连（QQ 邮箱 / 163 邮箱授权码方式）
//   - 587 / 25：STARTTLS（smtp 扩展协商；服务器不支持时明文发送）
//
// 仅实现发送，不接收邮件；UTF-8 中文内容 base64 编码，兼容主流客户端。
package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
)

// Config SMTP 发信配置。
type Config struct {
	Host     string
	Port     int
	User     string // 登录账号（QQ/163 邮箱为完整邮箱地址）
	Password string // 授权码（QQ/163 需在邮箱设置中生成）
	From     string // 发件人显示名（缺省用 User）
}

// Sender 发送接口（测试可注入 fake）。
type Sender interface {
	// Send 发送一封 UTF-8 文本邮件。
	Send(to, subject, body string) error
}

// SMTP 基于 net/smtp 的发送器。
type SMTP struct {
	cfg Config
}

// New 创建发送器（不做网络连接，配置错误在 Send 时暴露）。
func New(cfg Config) *SMTP {
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	return &SMTP{cfg: cfg}
}

// Send 发送邮件。
func (s *SMTP) Send(to, subject, body string) error {
	cfg := s.cfg
	if cfg.Host == "" || cfg.Port <= 0 {
		return fmt.Errorf("SMTP 配置不完整")
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	msg, err := buildMessage(cfg.From, to, subject, body)
	if err != nil {
		return err
	}

	var conn net.Conn
	if cfg.Port == 465 {
		conn, err = tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
	} else {
		conn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP 握手失败: %w", err)
	}
	defer client.Close()

	// 非 465 端口尝试 STARTTLS
	if cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return fmt.Errorf("STARTTLS 失败: %w", err)
			}
		}
	}
	if cfg.User != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("SMTP 认证失败（请检查账号/授权码）: %w", err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM 失败: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO 失败（收件地址被拒）: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		w.Close()
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("发送失败: %w", err)
	}
	return client.Quit()
}

// buildMessage 构造 UTF-8 base64 编码的 MIME 邮件（RFC 2047 主题）。
func buildMessage(from, to, subject, body string) ([]byte, error) {
	subjectEnc := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?="
	bodyEnc := base64.StdEncoding.EncodeToString([]byte(body))
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subjectEnc + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")
	sb.WriteString("\r\n")
	// base64 每 76 字符换行（RFC 2045）
	for len(bodyEnc) > 76 {
		sb.WriteString(bodyEnc[:76] + "\r\n")
		bodyEnc = bodyEnc[76:]
	}
	sb.WriteString(bodyEnc + "\r\n")
	return []byte(sb.String()), nil
}
