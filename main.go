// qqbot 是一个基于 OneBot v11（NapCat）的轻量 QQ 群管理机器人。
//
// 启动生命周期（见 docs/WEB_ADMIN_DESIGN.md 第 6 节）：
//   - 存在 data/control.json          → 网页化路径：control.json 为唯一配置源。
//   - 不存在但提供 QQBOT_IMPORT_CONFIG → 一次性迁移旧 config.yaml/runtime.json/
//     aliases.json 生成 control.json，然后走网页化路径。
//   - 都不满足                        → 旧版路径（config.yaml），作为回归基线，
//     并提示如何迁移。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"qqbot/internal/admin"
	"qqbot/internal/bot"
	"qqbot/internal/civgo"
	"qqbot/internal/config"
	"qqbot/internal/mailer"
	"qqbot/internal/onebot"
	"qqbot/internal/state"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func main() {
	if runCLI(os.Args[1:]) {
		return
	}
	var (
		verbose     = flag.Bool("v", false, "输出调试日志")
		adminListen = flag.String("admin-listen", envOr("QQBOT_ADMIN_LISTEN", "127.0.0.1:8080"), "管理后台监听地址（仅内网/VPN，勿暴露公网）")
		dataDir     = flag.String("data-dir", envOr("QQBOT_DATA_DIR", "data"), "数据目录（control.json 等）")
		masterKey   = flag.String("master-key-file", os.Getenv("QQBOT_MASTER_KEY_FILE"), "主密钥文件路径（网页化必需，与配置备份分开保存）")
		importCfg   = flag.String("import-config", os.Getenv("QQBOT_IMPORT_CONFIG"), "一次性导入旧 config.yaml 并生成 control.json")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	controlPath := filepath.Join(*dataDir, "control.json")
	switch {
	case fileExists(controlPath):
		// 网页化路径：control.json 为唯一配置源
		runManaged(*dataDir, *masterKey, *adminListen)
	case *importCfg != "":
		// 一次性迁移旧配置后走网页化路径
		runMigrateThenManaged(*dataDir, *masterKey, *adminListen, *importCfg)
	default:
		// 全新部署：初始化模式（只启动管理 HTTP，等待网页 setup）
		slog.Info("未发现 control.json 与 config.yaml：进入初始化模式（仅启动管理后台）")
		runManaged(*dataDir, *masterKey, *adminListen)
	}
}

// runManaged 网页化路径：control.json 为唯一配置源。
//   - 已初始化：创建 OneBot Manager 与 Bot，接入配置热生效，后台常驻连接循环。
//   - 未初始化（初始化模式）：只启动管理 HTTP 服务（setup/登录接口），
//     不创建 Bot、不连接 OneBot（设计文档 6.2）。
//
// 两种模式下管理 HTTP 服务（/api/v1）都启动；setup token 仅在首次启动输出一次。
func runManaged(dataDir, masterKeyFile, adminListen string) {
	keys, err := state.LoadMasterKey(masterKeyFile)
	if err != nil {
		slog.Error("加载主密钥失败（QQBOT_MASTER_KEY_FILE）", "err", err)
		os.Exit(1)
	}
	svc, err := state.Open(dataDir, keys)
	if err != nil {
		slog.Error("加载 control.json 失败", "err", err)
		os.Exit(1)
	}

	var manager *onebot.Manager
	var b *bot.Bot
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	adminSrv, err := admin.New(admin.Options{
		DataDir:        dataDir,
		Service:        svc,
		Keys:           keys,
		Manager:        manager,
		Actions:        actionsOf(b),
		QueryAudit:     queryAuditOf(b),
		RecentMessages: recentMessagesOf(b),
		SecureCookies:  secureCookies(),
		TrustedProxies: trustedProxies(),
	})
	if err != nil {
		slog.Error("初始化管理后台失败", "err", err)
		os.Exit(1)
	}

	// NapCat 掉线监控：邮箱 + NapCat 配置完整时自动启用（掉线发一封邮件提醒）
	adminSrv.StartNapCatWatchdog(ctx)

	// startComponents 创建 OneBot Manager 与 Bot，接入配置热生效（幂等，仅启动一次）。
	var startOnce sync.Once
	startComponents := func() {
		startOnce.Do(func() {
			eff := svc.Effective()
			timeout := time.Duration(eff.OneBot.APITimeoutMs) * time.Millisecond
			manager = onebot.NewManager(onebot.Endpoint{
				URL: eff.OneBot.WSURL, AccessToken: eff.OneBot.AccessToken, Timeout: timeout,
			})
			b = bot.NewWithDir(eff, manager, dataDir)
			// 配置热生效：control.json 提交成功后 → 更新 Bot 生效配置；
			// 仅当 OneBot endpoint 变化时才重连（群规则等修改不触发断线）
			svc.Subscribe(func() {
				next := svc.Effective()
				b.SetExternalConfigSource(func() *config.Config { return svc.Effective() })
				newEP := onebot.Endpoint{
					URL:         next.OneBot.WSURL,
					AccessToken: next.OneBot.AccessToken,
					Timeout:     time.Duration(next.OneBot.APITimeoutMs) * time.Millisecond,
				}
				cur := manager.Endpoint()
				if cur.URL != newEP.URL || cur.AccessToken != newEP.AccessToken || cur.Timeout != newEP.Timeout {
					manager.Reconfigure(newEP)
				}
			})
			b.Start()
			// civgo 社区服务：独立配置（data/civgo/civgo.json），未配置/未启用时零副作用。
			// 邮件通道复用 control.json 的 SMTP（与登录验证码/watchdog 同一通道），未配置时用量预警自动不启用。
			var sendMail func(to, subject, body string) error
			if e := svc.Effective().Email; e.SMTPHost != "" && e.SMTPPort > 0 && e.SMTPUser != "" && e.SMTPPassword != "" {
				sender := mailer.New(mailer.Config{
					Host: e.SMTPHost, Port: e.SMTPPort,
					User: e.SMTPUser, Password: e.SMTPPassword, From: e.SMTPUser,
				})
				defTo := svc.Effective().Watchdog.EmailTo // Effective() 已回退（watchdog.email_to → 系统邮箱收件人）
				sendMail = func(to, subject, body string) error {
					if to == "" {
						to = defTo
					}
					return sender.Send(to, subject, body)
				}
			}
			if cv, cerr := civgo.New(civgo.Options{DataDir: dataDir, Manager: manager, SendMail: sendMail}); cerr != nil {
				if !errors.Is(cerr, civgo.ErrNotConfigured) {
					slog.Error("civgo 模块初始化失败", "err", cerr)
				}
			} else {
				cv.Start(ctx)
			}
			go func() {
				if err := manager.Run(ctx); err != nil {
					slog.Error("OneBot 连接循环异常退出", "err", err)
				}
			}()
			adminSrv.SetComponents(manager, b.Actions(), b.QueryAudit, b.RecentMessages, b.CountersByGroup)
			slog.Info("机器人已启动（网页化路径）", "groups", len(eff.Groups))
		})
	}

	if svc.Initialized() {
		startComponents()
	} else {
		slog.Info("初始化模式：等待网页 setup 完成基础配置（不创建 Bot/OneBot）")
		// 配置首次提交成功后自动进入运行模式（无需重启进程）
		svc.Subscribe(func() {
			if svc.Initialized() {
				startComponents()
			}
		})
	}

	if adminSrv.SetupRequired() {
		tok, err := adminSrv.EnsureSetupToken()
		if err != nil {
			slog.Info("setup token 已存在（见历史启动日志），重启不会重新生成")
		} else {
			// 明文只输出到日志一次；磁盘只保存哈希
			slog.Info("========== 首次启动 setup token（仅显示一次）==========")
			slog.Info("SETUP_TOKEN", "token", tok)
			slog.Info("======================================================")
		}
	}

	httpSrv := &http.Server{
		Addr:              adminListen,
		Handler:           adminSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("管理后台已启动（仅内网/VPN 访问）", "addr", adminListen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("管理后台监听失败", "addr", adminListen, "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("收到退出信号，正在关闭")
	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	_ = httpSrv.Shutdown(shutdownCtx)
	slog.Info("已退出")
}

// actionsOf / queryAuditOf / recentMessagesOf：Bot 未创建（初始化模式）时返回 nil。
func actionsOf(b *bot.Bot) *bot.ActionService {
	if b == nil {
		return nil
	}
	return b.Actions()
}

func queryAuditOf(b *bot.Bot) func(bot.AuditQuery) bot.AuditPage {
	if b == nil {
		return nil
	}
	return b.QueryAudit
}

func recentMessagesOf(b *bot.Bot) func(int64, int) []bot.MessageRecord {
	if b == nil {
		return nil
	}
	return b.RecentMessages
}

// secureCookies 决定 Cookie Secure 标志：默认 true（文档 13.2）；
// 纯内网 HTTP 场景可用 QQBOT_ADMIN_COOKIE_INSECURE=1 关闭。
func secureCookies() bool {
	return os.Getenv("QQBOT_ADMIN_COOKIE_INSECURE") != "1"
}

// trustedProxies 解析 QQBOT_TRUST_PROXY（逗号分隔的可信反代 IP）。
// 仅这些来源的请求会信任 X-Forwarded-For（文档 13.4）。
func trustedProxies() []string {
	raw := os.Getenv("QQBOT_TRUST_PROXY")
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// runLoop 运行 OneBot 连接循环，阻塞直到退出信号。
func runLoop(parent context.Context, manager *onebot.Manager) {
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(ctx)
	}()
	select {
	case <-ctx.Done():
		slog.Info("收到退出信号，正在关闭")
	case err := <-errCh:
		if err != nil {
			slog.Error("客户端异常退出", "err", err)
		}
	}
	cancel()
	<-errCh
	slog.Info("已退出")
}

func ctx() context.Context { return context.Background() }

// runMigrateThenManaged 一次性迁移旧配置后走网页化路径。
func runMigrateThenManaged(dataDir, masterKeyFile, adminListen, importCfg string) {
	keys, err := state.LoadMasterKey(masterKeyFile)
	if err != nil {
		slog.Error("加载主密钥失败（迁移必需）", "err", err)
		os.Exit(1)
	}
	svc, err := state.Open(dataDir, keys)
	if err != nil {
		slog.Error("打开数据目录失败", "err", err)
		os.Exit(1)
	}
	candidate, err := state.Migrate(state.MigrateOptions{
		DataDir: dataDir, ConfigPath: importCfg, Keys: keys,
	})
	if err != nil {
		slog.Error("迁移失败：已中止，未写入 control.json", "err", err)
		os.Exit(1)
	}
	migrated := candidate
	if _, err := svc.Update("migration", 0, func(c *state.Control) error {
		*c = *migrated
		return nil
	}, "从 "+importCfg+" 一次性迁移"); err != nil {
		slog.Error("迁移提交失败：已中止", "err", err)
		os.Exit(1)
	}
	slog.Info("迁移完成：control.json 已生成",
		"path", filepath.Join(dataDir, "control.json"),
		"note", "请停止使用旧环境变量覆盖；旧 config.yaml 中的明文 secret 应尽快移除并轮换")
	runManaged(dataDir, masterKeyFile, adminListen)
}
