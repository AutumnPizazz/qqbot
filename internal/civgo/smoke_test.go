package civgo

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestSmokeRealDocs 真实文档冒烟：扫描 + 构建 docmap + 工具链路。
// 需要 testdata/ 下存在真实文档（本地无则跳过；部署前把 docs/game_content 拷入 testdata/ 跑一次）。
func TestSmokeRealDocs(t *testing.T) {
	dir := "testdata"
	files, err := scanDocs(dir)
	if err != nil || len(files) == 0 {
		t.Skipf("无真实文档: %v", err)
	}
	path := filepath.Join(t.TempDir(), "docmap.json")
	m, err := BuildDocmap(dir, path, nil)
	if err != nil {
		t.Fatalf("BuildDocmap 失败: %v", err)
	}
	total := 0
	for _, f := range files {
		df := m.Files[f]
		total += len(df.Headings)
		fmt.Printf("  %-28s → %d 行 / %d 标题\n", f, df.Lines, len(df.Headings))
	}
	fmt.Printf("总计: %d 文件 → %d 标题\n", len(files), total)
	if len(m.Files) == 0 {
		t.Error("docmap 不应为空")
	}
	// 工具链路冒烟：list_docs + 读取第一个文件
	cfg := DefaultConfig()
	dm := NewDocmapStore(path)
	ex := NewToolExecutor(dm, dir, func() *Config { return cfg })
	b := newContextBudget(cfg.Agent)
	out, err := ex.Execute("list_docs", "{}", b, 0)
	if err != nil {
		t.Fatalf("list_docs 失败: %v", err)
	}
	if !contains(out, "📄") {
		t.Errorf("list_docs 输出异常: %.200s", out)
	}
	first := files[0]
	if out2, _ := ex.Execute("read_doc", fmt.Sprintf(`{"path":%q}`, first), b, 0); !contains(out2, first) {
		t.Errorf("read_doc 输出异常: %.200s", out2)
	}
	_ = os.RemoveAll(path)
}
