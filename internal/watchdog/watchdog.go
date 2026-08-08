// Package watchdog 提供 NapCat 掉线监控：周期性检测 NapCat 登录状态，
// 掉线时向收件邮箱发送一封提醒邮件（每个掉线周期仅一封，恢复后自动重置）。
//
// 设计目标：
//   - 邮件通道复用管理后台的 SMTP 配置（授权码加密存储于 control.json，不进入代码/环境变量）；
//   - 检测复用 napcat.Client 的 CheckLogin（不额外探测端口）；
//   - 启动宽限期：进程重启瞬间 NapCat 可能尚未就绪或 QQ 正在自动登录，
//     宽限期内只观察不发提醒，确认在线或超时后才进入正常监控，避免重启误报。
package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DefaultInterval 默认检测间隔。
const DefaultInterval = 10 * time.Minute

// DefaultStartupGrace 默认启动宽限期：进程重启后 NapCat 拉起 + QQ 自动登录
// 通常需数十秒到几分钟，宽限期内未确认在线才视为真掉线（防漏报）。
const DefaultStartupGrace = 10 * time.Minute

// gracePollInterval 宽限期内的轮询间隔：远短于正常检测间隔，
// NapCat 一旦就绪即尽快确认在线、提前结束宽限期。
const gracePollInterval = 30 * time.Second

// Options 监控配置。
type Options struct {
	Interval     time.Duration // 检测间隔；<=0 时使用 DefaultInterval
	StartupGrace time.Duration // 启动宽限期；<=0 时使用 DefaultStartupGrace
	To           string        // 提醒收件邮箱（必填，来自系统邮箱配置）
	Logger       *slog.Logger  // nil 时使用 slog.Default()
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

// waitGrace 执行启动宽限期：以 poll 间隔轮询 check，
// 一旦确认在线立即返回（不发提醒）；宽限期耗尽仍未在线时调用一次 tick
// （视为真掉线，发出首封提醒）。ctx 取消立即返回。
func waitGrace(ctx context.Context, logger *slog.Logger, m *monitor, to string,
	check func(context.Context) (bool, error),
	send func(to, subject, body string) error, grace, poll time.Duration) {

	gctx, cancel := context.WithTimeout(ctx, grace)
	defer cancel()
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		online, err := check(gctx)
		if err == nil && online {
			logger.Info("NapCat 启动宽限期内确认在线，进入正常监控")
			return
		}
		select {
		case <-gctx.Done():
			if ctx.Err() != nil {
				return // 进程退出中（父 ctx 已取消），不再发信
			}
			logger.Warn("NapCat 启动宽限期结束仍未在线，按掉线处理", "err", err)
			m.tick(ctx, to, check, send) // 首次掉线提醒（内部去重，仅一封）
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Watch 阻塞运行监控循环：启动后先进入启动宽限期——进程重启瞬间 NapCat
// 可能尚未就绪或 QQ 正在自动登录，宽限期内只观察不发提醒，避免重启误报；
// 确认在线（提前结束宽限期）或宽限期超时（按真掉线提醒）后，按 Interval
// 周期检测；ctx 取消时退出。
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
	grace := opts.StartupGrace
	if grace <= 0 {
		grace = DefaultStartupGrace
	}
	if opts.To == "" {
		logger.Warn("watchdog: 收件邮箱为空，NapCat 掉线监控不会运行")
		return
	}

	m := newMonitor(logger)
	waitGrace(ctx, logger, m, opts.To, check, send, grace, gracePollInterval)

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
