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
	return setupRemoteBranch(t, "main")
}

// setupRemoteBranch 同 setupRemote，但可指定默认分支名。
func setupRemoteBranch(t *testing.T, branch string) string {
	t.Helper()
	gitAvailable(t)
	work := filepath.Join(t.TempDir(), "work")
	bare := filepath.Join(t.TempDir(), "remote.git")
	mustMkdir(t, filepath.Join(work, "docs", "game_content"))
	mustWrite(t, filepath.Join(work, "docs", "game_content", "archer.md"), "# 弓手\n弓手射程 2 格。\n")
	mustWrite(t, filepath.Join(work, "docs", "other.md"), "docs 下的其他文件\n")
	mustWrite(t, filepath.Join(work, "docs", "other_dir", "x.md"), "docs 下其他目录\n")
	mustWrite(t, filepath.Join(work, "README.md"), "civgo")
	runGit(t, work, "init", "-b", branch)
	runGit(t, work, "add", ".")
	runGit(t, work, "-c", "user.name=test", "-c", "user.email=t@t", "commit", "-m", "init")
	runGit(t, "", "init", "--bare", bare)
	runGit(t, work, "remote", "add", "origin", bare)
	runGit(t, work, "push", "origin", branch)
	// bare 仓库 HEAD 指向目标分支（默认指向不存在的 master，会导致 clone 无法 checkout）
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/"+branch)
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

// testSyncer 构造一个使用 fake 嵌入的 Syncer + Indexer + Store + DocmapStore（默认 main 分支）。
func testSyncer(t *testing.T, repoURL string) (*Syncer, *Indexer, *Store, string) {
	return testSyncerBranch(t, repoURL, "main")
}

// testSyncerBranch 同 testSyncer，可指定分支名（空 = 自动探测）。
func testSyncerBranch(t *testing.T, repoURL, branch string) (*Syncer, *Indexer, *Store, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AI.APIKey = "sk-test"
	cfg.Repo.URL = repoURL
	cfg.Repo.Branch = branch
	store := &Store{}
	store.cfgPtr.Store(cfg)

	embed := NewEmbedClient(cfg.AI)
	ce := &countEmbed{EmbedClient: embed, n: &atomic.Int64{}}
	dataDir := t.TempDir()
	ix := NewIndexer(ce, filepath.Join(dataDir, "civgo", "index.json"), cfg.Retrieval)
	ix.Load()
	dm := NewDocmapStore(filepath.Join(dataDir, "civgo", "docmap.json"))
	return NewSyncer(store, ix, dm, dataDir), ix, store, dataDir
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
	if st.LastDocmapSum == "" {
		t.Error("LastDocmapSum 应为非空")
	}
	// 文档地图就绪：archer.md 入图且带大纲
	dm := syn.docmap.Get()
	f, ok := dm.Files["archer.md"]
	if !ok {
		t.Fatalf("docmap 应包含 archer.md，got %v", dm.Files)
	}
	if len(f.Headings) == 0 || f.Headings[0].Text != "弓手" {
		t.Errorf("archer.md 大纲错误: %+v", f.Headings)
	}
	// 索引就绪（过渡期仍在建）
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
	dmAt := syn.loadState().LastDocmapAt
	time.Sleep(50 * time.Millisecond)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("无变化同步不应失败: %v", err)
	}
	if syn.loadState().LastHead != head {
		t.Error("无变化时 LastHead 不应改变")
	}
	if syn.loadState().LastDocmapAt != dmAt {
		t.Error("无变化时 LastDocmapAt 不应改变")
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

// TestBranchAutoDetectStable civgo 仓库实测默认分支为 stable（非 main）：
// 分支留空自动探测 → 应选 stable 并完成首次同步全流程。
func TestBranchAutoDetectStable(t *testing.T) {
	remote := setupRemoteBranch(t, "stable")
	ctx := context.Background()
	syn, ix, _, _ := testSyncerBranch(t, remote, "") // 分支留空 → 自动探测

	branch, err := syn.detectDefaultBranch(ctx, remote)
	if err != nil {
		t.Fatalf("探测分支失败: %v", err)
	}
	if branch != "stable" {
		t.Fatalf("应探测到 stable，got %s", branch)
	}
	// 全流程：首次同步（内部用探测到的分支 clone）
	if err := syn.syncOnce(ctx); err != nil {
		t.Fatalf("stable 仓库首次同步失败: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(syn.repoDir, "docs", "game_content", "archer.md"))
	if err != nil {
		t.Fatalf("clone 后文档缺失: %v", err)
	}
	if !strings.Contains(string(data), "弓手") {
		t.Errorf("文档内容错误: %s", data)
	}
	ix.mu.RLock()
	n := len(ix.entries)
	ix.mu.RUnlock()
	if n < 1 {
		t.Errorf("索引应有条目，got %d", n)
	}
}

// TestBranchAutoDetectBrokenHead 远端 HEAD 指向已删除分支（bare 仓库常见坑）：
// 应跳过不存在的 HEAD 指向，按优先级选择实际存在的分支。
func TestBranchAutoDetectBrokenHead(t *testing.T) {
	remote := setupRemoteBranch(t, "stable")
	// 把 bare HEAD 故意指向不存在的 master
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/master")
	ctx := context.Background()
	syn, _, _, _ := testSyncerBranch(t, remote, "")

	branch, err := syn.detectDefaultBranch(ctx, remote)
	if err != nil {
		t.Fatalf("探测分支失败: %v", err)
	}
	if branch != "stable" {
		t.Errorf("HEAD 指向失效时应回退到实际分支 stable，got %s", branch)
	}
	// 且全流程同步仍应成功
	if err := syn.syncOnce(ctx); err != nil {
		t.Fatalf("broken HEAD 下同步失败: %v", err)
	}
}

func TestSyncStatePersist(t *testing.T) {
	remote := setupRemote(t)
	syn, _, _, dataDir := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 新实例读取同一 state.json / docmap.json
	dm := NewDocmapStore(filepath.Join(dataDir, "civgo", "docmap.json"))
	syn2 := NewSyncer(syn.store, nil, dm, dataDir)
	st := syn2.loadState()
	if st.LastHead == "" || st.LastDocmapSum == "" {
		t.Errorf("状态持久化不完整: %+v", st)
	}
	if len(dm.Get().Files) < 1 {
		t.Error("docmap 持久化缺失")
	}
}

// TestSyncBranchLocalAfterClone 仓库已存在后分支读取走本地（零网络探测）：
// 远端 URL 不可达时应在 fetch 阶段失败（而非卡在 ls-remote 探测）。
func TestSyncBranchLocalAfterClone(t *testing.T) {
	remote := setupRemoteBranch(t, "stable")
	syn, _, _, _ := testSyncerBranch(t, remote, "") // 分支留空
	ctx := context.Background()
	if err := syn.syncOnce(ctx); err != nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	// 把 URL 改成不可达路径：若分支仍走网络探测会先失败在探测；
	// 本地分支缓存生效时应失败在 fetch
	cfg := syn.store.Get()
	cfg.Repo.URL = filepath.Join(t.TempDir(), "nonexist.git")
	err := syn.syncOnce(ctx)
	if err == nil {
		t.Fatal("不可达 URL 应失败")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("应失败在 fetch 阶段（本地分支缓存生效），got: %v", err)
	}
}

// TestSyncRebuildAfterDocmapLost 模拟「LastHead 已推进但文档地图从未成功构建」
// （如首次 clone 后构建失败，或 docmap 文件丢失）：即使远端无新提交也必须重建。
func TestSyncRebuildAfterDocmapLost(t *testing.T) {
	remote := setupRemote(t)
	syn, _, _, _ := testSyncer(t, remote)
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	// 模拟 docmap 丢失（store 清空），同时 state 里 LastDocmapAt 清零（从未成功构建过）
	syn.docmap.Replace(&Docmap{Version: docmapVersion, Files: map[string]DocmapFile{}})
	st := syn.loadState()
	st.LastDocmapAt = time.Time{}
	st.LastDocmapSum = ""
	syn.saveState(st)

	// 再次同步：远端无新提交，但文档地图必须被重建
	if err := syn.syncOnce(context.Background()); err != nil {
		t.Fatalf("重建同步失败: %v", err)
	}
	if len(syn.docmap.Get().Files) < 1 {
		t.Fatal("docmap 应被重建")
	}
	if syn.loadState().LastDocmapSum == "" {
		t.Error("重建后 LastDocmapSum 应非空")
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
	dm := NewDocmapStore(filepath.Join(dataDir, "civgo", "docmap.json"))
	syn := NewSyncer(store, ix, dm, dataDir)
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
