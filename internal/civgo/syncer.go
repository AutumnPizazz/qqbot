package civgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SyncState 同步状态（data/civgo/state.json）。
type SyncState struct {
	LastHead     string    `json:"last_head"` // 上次已同步的远端 commit
	LastSyncAt   time.Time `json:"last_sync_at"`
	LastError    string    `json:"last_error,omitempty"`
	FailCount    int       `json:"fail_count"`
	LastIndexAt  time.Time `json:"last_index_at"`
	LastIndexSum string    `json:"last_index_sum"`
}

// Syncer git 轮询同步器：首次 clone（浅 + sparse），之后定时 fetch →
// 检测 docs_path 提交变化 → merge --ff-only → 增量重建索引。
type Syncer struct {
	store     *Store
	index     *Indexer
	repoDir   string
	statePath string
	mu        sync.Mutex // 串行化 syncOnce
}

// NewSyncer 创建同步器。git 二进制缺失由 Service.New 提前探测。
func NewSyncer(store *Store, index *Indexer, dataDir string) *Syncer {
	return &Syncer{
		store:     store,
		index:     index,
		repoDir:   filepath.Join(dataDir, "civgo", "repo"),
		statePath: filepath.Join(dataDir, "civgo", "state.json"),
	}
}

// Run 同步主循环：首次立即同步，之后按配置间隔轮询；
// 每轮末尾检查配置热重载；连续失败按指数退避拉长间隔。
func (s *Syncer) Run(ctx context.Context) {
	cfg := s.store.Get()
	if err := s.syncOnce(ctx); err != nil {
		slog.Warn("civgo 首次同步失败", "err", err)
	}
	interval := time.Duration(cfg.Repo.SyncIntervalSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.store.ReloadIfChanged()
			cfg = s.store.Get()
			if !cfg.Enabled {
				continue
			}
			if err := s.syncOnce(ctx); err != nil {
				slog.Warn("civgo 同步失败", "err", err)
			}
			if ni := s.effectiveInterval(cfg); ni != interval {
				interval = ni
				ticker.Reset(interval)
				slog.Info("civgo 同步间隔调整", "interval_sec", int(interval.Seconds()))
			}
		}
	}
}

// effectiveInterval 按连续失败次数退避：≥3 次失败后间隔翻倍，上限 3600s。
func (s *Syncer) effectiveInterval(cfg *Config) time.Duration {
	base := time.Duration(cfg.Repo.SyncIntervalSec) * time.Second
	st := s.loadState()
	if st.FailCount < 3 {
		return base
	}
	mult := 1 << min(st.FailCount-2, 4)
	iv := base * time.Duration(mult)
	if iv > time.Hour {
		iv = time.Hour
	}
	return iv
}

// syncOnce 单轮同步（幂等，可重复调用）。
func (s *Syncer) syncOnce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.store.Get()
	if !cfg.Enabled {
		return nil
	}
	if cfg.Repo.URL == "" {
		return fmt.Errorf("repo.url 为空")
	}

	branch := cfg.Repo.Branch
	if branch == "" {
		b, err := s.detectDefaultBranch(ctx, cfg.Repo.URL)
		if err != nil {
			return s.fail(fmt.Errorf("探测默认分支失败: %w", err))
		}
		branch = b
	}

	if _, err := os.Stat(filepath.Join(s.repoDir, ".git")); err != nil {
		// 首次：clone
		if cerr := s.clone(ctx, cfg, branch); cerr != nil {
			return s.fail(cerr)
		}
		head, herr := s.gitHead(ctx, "HEAD")
		if herr != nil {
			return s.fail(herr)
		}
		slog.Info("civgo 首次克隆完成", "head", shortHead(head))
		return s.indexAndRecord(ctx, cfg, head)
	}

	// 轮询：先对齐 remote URL（配置变更即时生效，幂等）→ fetch → 检测 → merge
	if _, _, err := s.git(ctx, s.repoDir, "remote", "set-url", "origin", cfg.Repo.URL); err != nil {
		return s.fail(fmt.Errorf("remote set-url 失败: %w", err))
	}
	if _, _, err := s.git(ctx, s.repoDir, "fetch", "origin", branch); err != nil {
		return s.fail(fmt.Errorf("fetch 失败: %w", err))
	}
	// 注意：浅克隆 fetch 后本地 HEAD 不自动前进，必须用 FETCH_HEAD 作为远端最新提交
	head, err := s.gitHead(ctx, "FETCH_HEAD")
	if err != nil {
		return s.fail(err)
	}
	st := s.loadState()
	if st.LastHead == head {
		st.LastSyncAt = time.Now()
		st.FailCount = 0
		st.LastError = ""
		s.saveState(st)
		return nil // 远端无新提交，轻量返回
	}

	// 检测 docs_path 是否有提交变化（双保险：LastHead 变化但可能只改了 docs 之外）
	out, _, err := s.git(ctx, s.repoDir, "log", "--format=%H", "HEAD..FETCH_HEAD", "--", cfg.Repo.DocsPath)
	if err != nil {
		return s.fail(err)
	}
	if strings.TrimSpace(out) == "" {
		st.LastHead = head
		st.LastSyncAt = time.Now()
		st.FailCount = 0
		st.LastError = ""
		s.saveState(st)
		return nil // docs 无变化，仅推进 LastHead
	}

	// 快进合并；本地被意外修改导致冲突时 reset 后重试一次
	if _, _, err := s.git(ctx, s.repoDir, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		slog.Warn("civgo merge 失败（尝试 reset 恢复）", "err", err)
		if _, _, rerr := s.git(ctx, s.repoDir, "reset", "--hard", "HEAD"); rerr != nil {
			return s.fail(fmt.Errorf("reset 失败: %w", rerr))
		}
		if _, _, err2 := s.git(ctx, s.repoDir, "merge", "--ff-only", "FETCH_HEAD"); err2 != nil {
			return s.fail(fmt.Errorf("merge 重试失败: %w", err2))
		}
	}
	return s.indexAndRecord(ctx, cfg, head)
}

// clone 首次克隆（浅克隆 + 可选 sparse checkout 只取 docs 目录）。
func (s *Syncer) clone(ctx context.Context, cfg *Config, branch string) error {
	args := []string{"clone"}
	if cfg.Repo.CloneShallow {
		args = append(args, "--depth", "1")
	}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	if cfg.Repo.SparseCheckout {
		args = append(args, "--filter=blob:none", "--sparse")
	}
	args = append(args, cfg.Repo.URL, s.repoDir)
	if _, _, err := s.git(ctx, "", args...); err != nil {
		return fmt.Errorf("clone 失败: %w", err)
	}
	if cfg.Repo.SparseCheckout {
		// 非 cone 精确模式：只检出 docs 目录（cone 模式会附带根文件与同级文件）。
		// 模式文件直接写入 .git/info/sparse-checkout 再 reapply，
		// 避免不同平台对命令行路径参数的处理差异（如 Windows MSYS 路径转换）。
		if _, _, err := s.git(ctx, s.repoDir, "sparse-checkout", "init", "--no-cone"); err != nil {
			return fmt.Errorf("sparse-checkout init 失败: %w", err)
		}
		pattern := "/" + strings.Trim(strings.TrimPrefix(cfg.Repo.DocsPath, "/"), "/") + "/\n"
		if err := os.WriteFile(filepath.Join(s.repoDir, ".git", "info", "sparse-checkout"), []byte(pattern), 0o644); err != nil {
			return fmt.Errorf("写入 sparse-checkout 模式失败: %w", err)
		}
		if _, _, err := s.git(ctx, s.repoDir, "sparse-checkout", "reapply"); err != nil {
			return fmt.Errorf("sparse-checkout reapply 失败: %w", err)
		}
	}
	return nil
}

// detectDefaultBranch 通过 ls-remote --symref 探测远端默认分支。
func (s *Syncer) detectDefaultBranch(ctx context.Context, url string) (string, error) {
	out, _, err := s.git(ctx, "", "ls-remote", "--symref", url, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ref:") && strings.HasSuffix(line, "HEAD") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return strings.TrimPrefix(fields[1], "refs/heads/"), nil
			}
		}
	}
	return "main", nil // 兜底
}

// gitHead 解析仓库中指定引用（HEAD 或 FETCH_HEAD）的 commit hash。
func (s *Syncer) gitHead(ctx context.Context, ref string) (string, error) {
	out, _, err := s.git(ctx, s.repoDir, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// indexAndRecord 增量重建索引并落盘状态。
func (s *Syncer) indexAndRecord(ctx context.Context, cfg *Config, head string) error {
	docsDir := filepath.Join(s.repoDir, cfg.Repo.DocsPath)
	sum, err := s.index.RebuildChanged(ctx, docsDir)
	if err != nil {
		return s.fail(err)
	}
	st := s.loadState()
	if head != "" {
		st.LastHead = head
	}
	st.LastSyncAt = time.Now()
	st.LastIndexAt = time.Now()
	st.LastIndexSum = sum.String()
	st.FailCount = 0
	st.LastError = ""
	s.saveState(st)
	slog.Info("civgo 同步完成", "head", shortHead(head), "summary", sum.String())
	return nil
}

// fail 记录失败状态（FailCount 递增、LastError）并返回错误。
func (s *Syncer) fail(err error) error {
	st := s.loadState()
	st.FailCount++
	st.LastError = err.Error()
	st.LastSyncAt = time.Now()
	s.saveState(st)
	return err
}

// loadState 读取同步状态；缺失/损坏返回空状态。
func (s *Syncer) loadState() SyncState {
	var st SyncState
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		slog.Warn("civgo 同步状态解析失败（重置）", "err", err)
	}
	return st
}

// saveState 原子写同步状态。
func (s *Syncer) saveState(st SyncState) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.statePath), ".state-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, s.statePath)
}

// git 执行 git 命令；dir 为空表示不指定工作目录。
func (s *Syncer) git(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		slog.Debug("civgo git 失败", "args", args, "err", err, "stderr", truncateStr(stderr.String(), 300))
	}
	return stdout.String(), stderr.String(), err
}

// shortHead 缩短 commit hash 用于日志。
func shortHead(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
