package civgo

import (
	"path/filepath"
	"strings"
	"testing"
)

// testToolEnv 构造工具执行环境：docmap + 文档目录 + 预算。
func testToolEnv(t *testing.T) (*ToolExecutor, *ContextBudget) {
	t.Helper()
	dir := writeDocTree(t)
	cfg := DefaultConfig()
	path := filepath.Join(t.TempDir(), "civgo", "docmap.json")
	if _, err := BuildDocmap(dir, path, nil); err != nil {
		t.Fatal(err)
	}
	dm := NewDocmapStore(path)
	ex := NewToolExecutor(dm, dir, func() *Config { return cfg })
	return ex, newContextBudget(cfg.Agent)
}

func TestListDocs(t *testing.T) {
	ex, b := testToolEnv(t)
	out, err := ex.Execute("list_docs", "{}", b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "📁 units/") {
		t.Errorf("应列出子目录: %s", out)
	}
	if !strings.Contains(out, "archer.md") || !strings.Contains(out, "7 行") {
		t.Errorf("应列出根下文件及行数: %s", out)
	}
	if !strings.Contains(out, "索引.md") || !strings.Contains(out, "⭐") {
		t.Errorf("索引文件应带 ⭐: %s", out)
	}
	if strings.Contains(out, "ignore.tmp") {
		t.Errorf("非支持类型不应列出: %s", out)
	}
	// 下钻
	out2, _ := ex.Execute("list_docs", `{"path":"units"}`, b)
	if !strings.Contains(out2, "knight.md") {
		t.Errorf("下钻应列出 units 下文件: %s", out2)
	}
	if strings.Contains(out2, "archer.md") {
		t.Errorf("下钻不应含父级文件: %s", out2)
	}
	// 无效路径
	out3, _ := ex.Execute("list_docs", `{"path":"../etc"}`, b)
	if !strings.Contains(out3, "无效") {
		t.Errorf("路径逃逸应拒绝: %s", out3)
	}
}

func TestGetDocOutline(t *testing.T) {
	ex, b := testToolEnv(t)
	out, err := ex.Execute("get_doc_outline", `{"path":"archer.md"}`, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "archer.md") || !strings.Contains(out, "第 1 行 [#] 弓手") {
		t.Errorf("大纲输出错误: %s", out)
	}
	// 不存在文件
	out2, _ := ex.Execute("get_doc_outline", `{"path":"nope.md"}`, b)
	if !strings.Contains(out2, "不存在") {
		t.Errorf("不存在文件应提示: %s", out2)
	}
	// 缺 path 参数
	out3, _ := ex.Execute("get_doc_outline", `{}`, b)
	if !strings.Contains(out3, "无效") {
		t.Errorf("缺 path 应提示无效: %s", out3)
	}
}

func TestReadDoc(t *testing.T) {
	ex, b := testToolEnv(t)
	// 全文（小文件）
	out, err := ex.Execute("read_doc", `{"path":"archer.md"}`, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "第 1~7 行") || !strings.Contains(out, "1: # 弓手") {
		t.Errorf("读取输出错误: %s", out)
	}
	// 分页：start_line/max_lines
	out2, _ := ex.Execute("read_doc", `{"path":"archer.md","start_line":4,"max_lines":2}`, b)
	if !strings.Contains(out2, "第 4~5 行") || !strings.Contains(out2, "4: ## 技能体系") {
		t.Errorf("分页读取错误: %s", out2)
	}
	// 超范围
	out3, _ := ex.Execute("read_doc", `{"path":"archer.md","start_line":99}`, b)
	if !strings.Contains(out3, "超出范围") {
		t.Errorf("超范围应提示: %s", out3)
	}
	// 逃逸
	out4, _ := ex.Execute("read_doc", `{"path":"../../etc/passwd"}`, b)
	if !strings.Contains(out4, "无效") {
		t.Errorf("逃逸路径应拒绝: %s", out4)
	}
	// 不存在
	out5, _ := ex.Execute("read_doc", `{"path":"nope.md"}`, b)
	if !strings.Contains(out5, "读取失败") {
		t.Errorf("不存在应提示读取失败: %s", out5)
	}
}

func TestReadDocBudget(t *testing.T) {
	ex, _ := testToolEnv(t)
	// 预算极小：首次读取执行后预算即满，第二次读取拒绝
	cfg := DefaultConfig()
	cfg.Agent.MaxContextChars = 10
	b := newContextBudget(cfg.Agent)
	out, _ := ex.Execute("read_doc", `{"path":"archer.md"}`, b)
	if strings.Contains(out, "预算已满") {
		t.Errorf("首次读取不应被预算拒绝: %s", out)
	}
	if b.readUsed < 10 {
		t.Errorf("读取后预算应已累计: %d", b.readUsed)
	}
	out2, _ := ex.Execute("read_doc", `{"path":"archer.md"}`, b)
	if !strings.Contains(out2, "预算已满") {
		t.Errorf("预算耗尽后应拒绝读取: %s", out2)
	}
	// 手动耗尽预算
	b2 := newContextBudget(DefaultConfig().Agent)
	b2.readUsed = b2.readMax
	out3, _ := ex.Execute("read_doc", `{"path":"archer.md"}`, b2)
	if !strings.Contains(out3, "预算已满") {
		t.Errorf("预算耗尽后应拒绝: %s", out3)
	}
}

func TestExecuteUnknownTool(t *testing.T) {
	ex, b := testToolEnv(t)
	_, err := ex.Execute("no_such_tool", "{}", b)
	if err == nil || !strings.Contains(err.Error(), "未知工具") {
		t.Fatalf("未知工具应报错: %v", err)
	}
}

func TestExecuteBadJSONArgs(t *testing.T) {
	ex, b := testToolEnv(t)
	// 坏 JSON 按空参数处理（list_docs 无参 = 根目录），不 panic
	out, err := ex.Execute("list_docs", `{bad json`, b)
	if err != nil {
		t.Fatalf("坏 JSON 不应报错: %v", err)
	}
	if !strings.Contains(out, "文档目录") {
		t.Errorf("坏 JSON 应按空参数执行: %s", out)
	}
}

func TestSafeJoin(t *testing.T) {
	base := "/data/civgo/repo/docs"
	cases := []struct {
		rel  string
		want bool
	}{
		{"units/archer.md", true},
		{"./units/archer.md", true},
		{"../secret", false},
		{"../../etc/passwd", false},
		{"/abs/path", false},
		{"", false},
	}
	for _, tc := range cases {
		_, ok := safeJoin(base, tc.rel)
		if ok != tc.want {
			t.Errorf("safeJoin(%q) = %v, want %v", tc.rel, ok, tc.want)
		}
	}
}
