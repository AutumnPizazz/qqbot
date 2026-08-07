package civgo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- 本地 bare repo fixture（要求本机有 git；无 git 时跳过） ----

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("本机无 git，跳过同步器测试")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v 失败: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupRemote 建一个带 docs/game_content 初始提交的本地 bare 仓库，返回其路径。
func setupRemote(t *testing.T) string {
	t.Helper()
	gitAvailable(t)
	work := filepath.Join(t.TempDir(), "work")
	bare := filepath.Join(t.TempDir(), "remote.git")
	mustMkdir(t, filepath.Join(work, "docs", "game_content"))
	mustWrite(t, filepath.Join(work, "docs", "game_content", "archer.md"), "# 弓手\n弓手射程 2 格。\n")
	mustWrite(t, filepath.Join(work, "docs", "other.md"), "docs 下的其他文件\n")
	mustWrite(t, filepath.Join(work, "docs", "other_dir", "x.md"), "docs 下其他目录\n")
	mustWrite(t, filepath.Join(work, "README.md"), "civgo")
	runGit(t, work, "init", "-b", "main")
	runGit(t, work, "add", ".")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=t@t", "commit", "-m", "init")
	runGit(t, "", "init", "--bare", bare)
	runGit(t, work, "remote", "add", "origin", bare)
	runGit(t, work, "push", "origin", "main")
	// bare 仓库 HEAD 指向 main（默认指向不存在的 master，会导致 clone 无法 checkout）
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return bare
}

// commitDoc 在远端工作区新增/修改文档并推送。
func commitDoc(t *testing.T, remoteWorkDir, rel, content, msg string) {
	t.Helper()
	mustWrite(t, filepath.Join(remoteWorkDir, rel), content)
	runGit(t, remoteWorkDir, "add", rel)
	runGit(t, remoteWorkDir, "-c", "user.name=test", "-c", "user.email=t@t", "commit", "-m", msg)
	runGit(t, remoteWorkDir, "push", "origin", "main")
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testSyncer 构造一个使用 fake 嵌入的 Syncer + Indexer + Store。
func testSyncer(t *testing.T, repoURL string) (*Syncer, *Indexer, *Store, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AI.APIKey = "sk-test"
	cfg.Repo.URL = repoURL
	cfg.Repo.Branch = "main"
	store := &Store{}
	store.cfgPtr.Store(cfg)

	embed := NewEmbedClient(cfg.AI)
	ce := &countEmbed{EmbedClient: embed, n: &atomic.Int64{}}
	dataDir := t.TempDir()
	ix := NewIndexer(ce, filepath.Join(dataDir, "civgo", "index.json"), cfg.Retrieval)
	ix.Load()
	return NewSyncer(store, ix, dataDir), ix, store, dataDir
}

func TestSyncFirstCloneAndIndex(t *testing.T) {
	remote := setupRemote(t)
	syn, ix, _, _ := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	// 工作树出现文档
	data, err := os.ReadFile(filepath.Join(syn.repoDir, "docs", "game_content", "archer.md"))
	if err != nil {
		t.Fatalf("clone 后文档缺失: %v", err)
	}
	if !strings.Contains(string(data), "弓手") {
		t.Errorf("文档内容错误: %s", data)
	}
	// 状态落盘
	st := syn.loadState()
	if st.LastHead == "" {
		t.Error("LastHead 应为非空")
	}
	if st.LastIndexSum == "" {
		t.Error("LastIndexSum 应为非空")
	}
	// 索引就绪
	ix.mu.RLock()
	n := len(ix.entries)
	ix.mu.RUnlock()
	if n < 1 {
		t.Errorf("索引应有条目，got %d", n)
	}
}

func TestSyncDetectChange(t *testing.T) {
	remote := setupRemote(t)
	work := filepath.Join(t.TempDir(), "w")
	runGit(t, "", "clone", "-q", remote, work)
	syn, _, _, _ := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldHead := syn.loadState().LastHead

	// 远端新增提交（修改 archer.md）
	commitDoc(t, work, "docs/game_content/archer.md", "# 弓手\n弓手射程 3 格，攻击力 6。\n", "改弓手")
	syn2, _, _, _ := testSyncer(t, remote)
	_ = syn2
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("增量同步失败: %v", err)
	}
	st := syn.loadState()
	if st.LastHead == oldHead {
		t.Error("LastHead 应更新")
	}
	data, _ := os.ReadFile(filepath.Join(syn.repoDir, "docs", "game_content", "archer.md"))
	if !strings.Contains(string(data), "攻击力 6") {
		t.Errorf("本地文档应更新: %s", data)
	}
}

func TestSyncNoChangeSkipped(t *testing.T) {
	remote := setupRemote(t)
	syn, _, _, _ := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	head := syn.loadState().LastHead
	time.Sleep(50 * time.Millisecond)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("无变化同步不应失败: %v", err)
	}
	if syn.loadState().LastHead != head {
		t.Error("无变化时 LastHead 不应改变")
	}
	if syn.loadState().FailCount != 0 {
		t.Error("无变化不应计失败")
	}
}

func TestSyncDocsOnlyChange(t *testing.T) {
	// 只改 docs 之外的文件（README）：LastHead 推进但 docs 内容不更新、不重建索引
	remote := setupRemote(t)
	work := filepath.Join(t.TempDir(), "w")
	runGit(t, "", "clone", "-q", remote, work)
	syn, ix, _, _ := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	commitDoc(t, work, "README.md", "civgo v2", "只改 README")
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	st := syn.loadState()
	if st.LastHead == "" {
		t.Fatal("LastHead 不应为空")
	}
	ix.mu.RLock()
	n := len(ix.entries)
	ix.mu.RUnlock()
	if n < 1 {
		t.Errorf("索引不应被清空，got %d", n)
	}
}

func TestSyncConflictReset(t *testing.T) {
	remote := setupRemote(t)
	work := filepath.Join(t.TempDir(), "w")
	runGit(t, "", "clone", "-q", remote, work)
	syn, _, _, _ := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 本地工作树被意外修改（未提交，导致 merge 冲突）
	mustWrite(t, filepath.Join(syn.repoDir, "docs", "game_content", "archer.md"), "本地乱改")
	// 远端新提交
	commitDoc(t, work, "docs/game_content/archer.md", "# 弓手\n弓手射程 9 格。\n", "远端改")
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("冲突应被 reset 自救: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(syn.repoDir, "docs", "game_content", "archer.md"))
	if !strings.Contains(string(data), "射程 9 格") {
		t.Errorf("reset 后应为远端版本: %s", data)
	}
}

func TestSyncFetchFailureBackoff(t *testing.T) {
	remote := setupRemote(t)
	syn, _, _, _ := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 把 URL 改成不存在的仓库 → set-url 后 fetch 失败
	cfg := syn.store.Get()
	cfg.Repo.URL = filepath.Join(t.TempDir(), "nonexist.git")
	for i := 0; i < 4; i++ {
		if err := syn.syncOnce(context.Background()); err == nil {
			t.Fatalf("第 %d 轮应失败", i+1)
		}
	}
	st := syn.loadState()
	if st.FailCount != 4 {
		t.Errorf("FailCount 应为 4，got %d", st.FailCount)
	}
	if st.LastError == "" {
		t.Error("LastError 应为非空")
	}
	// 退避间隔：fail=4 → 2^(4-2)=4 倍
	iv := syn.effectiveInterval(cfg)
	if iv != time.Duration(cfg.Repo.SyncIntervalSec)*time.Second*4 {
		t.Errorf("退避间隔错误: %v", iv)
	}
}

func TestSyncFailThenRecover(t *testing.T) {
	remote := setupRemote(t)
	syn, _, _, _ := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 失败一次
	cfg := syn.store.Get()
	cfg.Repo.URL = filepath.Join(t.TempDir(), "nonexist.git")
	_ = syn.syncOnce(context.Background())
	// 恢复
	cfg.Repo.URL = remote
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("恢复后同步失败: %v", err)
	}
	if syn.loadState().FailCount != 0 {
		t.Error("成功后 FailCount 应清零")
	}
}

func TestBranchAutoDetect(t *testing.T) {
	remote := setupRemote(t)
	ctx := context.Background()
	syn, _, _, _ := testSyncer(t, remote)
	branch, err := syn.detectDefaultBranch(ctx, remote)
	if err != nil {
		t.Fatalf("探测分支失败: %v", err)
	}
	if branch != "main" {
		t.Errorf("应探测到 main，got %s", branch)
	}
}

func TestSyncStatePersist(t *testing.T) {
	remote := setupRemote(t)
	syn, _, _, dataDir := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 新实例读取同一 state.json
	syn2 := NewSyncer(syn.store, nil, dataDir)
	st := syn2.loadState()
	if st.LastHead == "" || st.LastIndexSum == "" {
		t.Errorf("状态持久化不完整: %+v", st)
	}
}

func TestSyncSparseCheckout(t *testing.T) {
	remote := setupRemote(t)
	cfg := DefaultConfig()
	cfg.AI.APIKey = "sk-test"
	cfg.Repo.URL = remote
	cfg.Repo.Branch = "main"
	cfg.Repo.SparseCheckout = true
	store := &Store{}
	store.cfgPtr.Store(cfg)
	embed := NewEmbedClient(cfg.AI)
	ce := &countEmbed{EmbedClient: embed, n: &atomic.Int64{}}
	dataDir := t.TempDir()
	ix := NewIndexer(ce, filepath.Join(dataDir, "civgo", "index.json"), cfg.Retrieval)
	syn := NewSyncer(store, ix, dataDir)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("sparse 同步失败: %v", err)
	}
	// 精确 sparse：工作树只含 docs/game_content（根文件/同级文件/兄弟目录都不应出现）
	if _, err := os.Stat(filepath.Join(syn.repoDir, "docs", "game_content", "archer.md")); err != nil {
		t.Fatalf("sparse 后目标文档缺失: %v", err)
	}
	for _, unwanted := range []string{
		"README.md",
		"docs/other.md",
		"docs/other_dir/x.md",
	} {
		if _, err := os.Stat(filepath.Join(syn.repoDir, unwanted)); err == nil {
			t.Errorf("精确 sparse 下 %s 不应出现在工作树", unwanted)
		}
	}
}
