// Package watchdog 提供 NapCat 掉线监控：周期性检测 NapCat 登录状态，
// 掉线时向收件邮箱发送一封提醒邮件（每个掉线周期仅一封，恢复后自动重置）。
//
// 设计目标：
//   - 邮件通道复用管理后台的 SMTP 配置（授权码加密存储于 control.json，不进入代码/环境变量）；
//   - 检测复用 napcat.Client 的 CheckLogin（不额外探测端口）；
//   - 纯内存状态，进程重启后若仍掉线会重新提醒一次（可接受，防漏报）。
package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DefaultInterval 默认检测间隔。
const DefaultInterval = 10 * time.Minute

// Options 监控配置。
type Options struct {
	Interval time.Duration // 检测间隔；<=0 时使用 DefaultInterval
	To       string        // 提醒收件邮箱（必填，来自系统邮箱配置）
	Logger   *slog.Logger  // nil 时使用 slog.Default()
}

// monitor 是去重状态机（独立于循环，便于单元测试）。
type monitor struct {
	down     bool // 当前是否处于掉线状态
	notified bool // 本次掉线周期是否已发送提醒
	logger   *slog.Logger
}

func newMonitor(logger *slog.Logger) *monitor {
	return &monitor{logger: logger}
}

// tick 执行一次检测并驱动状态机。返回是否发送了提醒邮件。
func (m *monitor) tick(ctx context.Context, to string,
	check func(context.Context) (bool, error),
	send func(to, subject, body string) error) bool {

	online, err := check(ctx)
	if err != nil || !online {
		if !m.down {
			m.logger.Warn("NapCat 掉线", "err", err)
			m.down = true
		}
		if m.notified {
			return false // 已提醒过，静默
		}
		body := fmt.Sprintf(
			"【QQBot 提醒】NapCat 已掉线，需要扫码恢复。\n\n"+
				"打开管理后台首页「NapCat 登录」卡片，用手机 QQ 扫码即可恢复。\n\n"+
				"（此提醒每个掉线周期只发送一次，扫码恢复后自动重置）")
		if serr := send(to, "【QQBot】NapCat 掉线，请扫码恢复", body); serr != nil {
			m.logger.Error("掉线提醒邮件发送失败", "err", serr)
			return false
		}
		m.notified = true
		m.logger.Info("掉线提醒已发送", "to", to)
		return true
	}
	if m.down {
		m.logger.Info("NapCat 已恢复在线，提醒状态重置")
	}
	m.down = false
	m.notified = false
	return false
}

// Watch 阻塞运行监控循环：启动时立即检测一次，之后按 Interval 周期检测；
// ctx 取消时退出。
func Watch(ctx context.Context, check func(context.Context) (bool, error),
	send func(to, subject, body string) error, opts Options) {

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	if opts.To == "" {
		logger.Warn("watchdog: 收件邮箱为空，NapCat 掉线监控不会运行")
		return
	}

	m := newMonitor(logger)
	m.tick(ctx, opts.To, check, send)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick(ctx, opts.To, check, send)
		}
	}
}
