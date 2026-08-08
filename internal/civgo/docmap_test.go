package civgo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDocTree 构造测试文档树，返回 docsDir。
func writeDocTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "archer.md"),
		"# 弓手\n弓手是远程单位。\n\n## 技能体系\n\n### 二连射\n消耗 10 蓝。\n")
	mustWrite(t, filepath.Join(dir, "units", "knight.md"),
		"# 骑士\n近战单位。\n\n## 冲锋\n位移技能。\n")
	mustWrite(t, filepath.Join(dir, "索引.md"),
		"# 阅读顺序\n先从基础开始。\n")
	mustWrite(t, filepath.Join(dir, "ignore.tmp"), "不应被扫描")
	return dir
}

func TestExtractHeadings(t *testing.T) {
	content := "# 标题一\r\n正文\r\n## 标题二\r\n### 标题三\r\n"
	hs, lines := extractHeadings(content)
	if lines != 4 {
		t.Errorf("行数应为 4，got %d", lines)
	}
	want := []DocHeading{
		{Line: 1, Level: 1, Text: "标题一"},
		{Line: 3, Level: 2, Text: "标题二"},
		{Line: 4, Level: 3, Text: "标题三"},
	}
	if len(hs) != len(want) {
		t.Fatalf("标题数应为 %d，got %d: %+v", len(want), len(hs), hs)
	}
	for i, w := range want {
		if hs[i] != w {
			t.Errorf("第 %d 个标题应为 %+v，got %+v", i, w, hs[i])
		}
	}
	// 非标题行、代码块中的 # 不应误判
	hs2, _ := extractHeadings("普通行\n```\n# 代码块里的\n```\n")
	if len(hs2) != 0 {
		t.Errorf("代码块内 # 不应算标题: %+v", hs2)
	}
}

func TestBuildDocmap(t *testing.T) {
	dir := writeDocTree(t)
	path := filepath.Join(t.TempDir(), "civgo", "docmap.json")
	m, err := BuildDocmap(dir, path, nil)
	if err != nil {
		t.Fatalf("BuildDocmap 失败: %v", err)
	}
	// 只收录支持类型（.tmp 跳过）
	if len(m.Files) != 3 {
		t.Fatalf("应收录 3 个文件，got %d: %+v", len(m.Files), m.Files)
	}
	archer := m.Files["archer.md"]
	if archer.Lines != 7 {
		t.Errorf("archer.md 行数应为 7，got %d", archer.Lines)
	}
	if len(archer.Headings) != 3 || archer.Headings[0].Text != "弓手" || archer.Headings[1].Line != 4 {
		t.Errorf("archer.md 大纲错误: %+v", archer.Headings)
	}
	if archer.IndexFile {
		t.Error("archer.md 不应标记为索引文件")
	}
	if !m.Files["索引.md"].IndexFile {
		t.Error("索引.md 应标记为索引文件")
	}
	// 落盘可重载
	loaded := NewDocmapStore(path)
	if got := loaded.Get().Files["units/knight.md"].Headings; len(got) != 2 {
		t.Errorf("重载后 knight 大纲错误: %+v", got)
	}
}

func TestBuildDocmapIncremental(t *testing.T) {
	dir := writeDocTree(t)
	path := filepath.Join(t.TempDir(), "civgo", "docmap.json")
	m, err := BuildDocmap(dir, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldHash := m.Files["archer.md"].Hash

	// 修改一个文件、新增一个文件、删除一个文件
	mustWrite(t, filepath.Join(dir, "archer.md"), "# 弓手 v2\n射程改了。\n")
	mustWrite(t, filepath.Join(dir, "new.md"), "# 新内容\n")
	if err := os.Remove(filepath.Join(dir, "units", "knight.md")); err != nil {
		t.Fatal(err)
	}
	m2, err := BuildDocmap(dir, path, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m2.Files["units/knight.md"]; ok {
		t.Error("已删除文件不应再出现")
	}
	if _, ok := m2.Files["new.md"]; !ok {
		t.Error("新增文件应出现")
	}
	a := m2.Files["archer.md"]
	if a.Hash == oldHash || a.Lines != 2 {
		t.Errorf("修改文件应重建大纲: lines=%d hashChanged=%v", a.Lines, a.Hash != oldHash)
	}
	if len(a.Headings) != 1 {
		t.Errorf("archer.md v2 大纲应为 1 条，got %+v", a.Headings)
	}
	// 未变化文件保留旧条目
	if m2.Files["索引.md"].Hash != m.Files["索引.md"].Hash {
		t.Error("未变化文件应复用旧条目")
	}
}

func TestBuildDocmapEmptyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no_such_dir")
	path := filepath.Join(t.TempDir(), "civgo", "docmap.json")
	m, err := BuildDocmap(dir, path, nil)
	if err != nil {
		t.Fatalf("空目录不应报错: %v", err)
	}
	if len(m.Files) != 0 {
		t.Error("空目录地图应为空")
	}
}

func TestDocmapStoreCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "civgo", "docmap.json")
	mustWrite(t, path, "{corrupt")
	s := NewDocmapStore(path)
	if len(s.Get().Files) != 0 {
		t.Error("损坏文件应回退空地图")
	}
	// 且 Replace 后恢复
	m := &Docmap{Version: docmapVersion, Files: map[string]DocmapFile{"a.md": {Lines: 1}}}
	s.Replace(m)
	if len(s.Get().Files) != 1 {
		t.Error("Replace 后应生效")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "a.md") {
		t.Errorf("Replace 应落盘: %v", err)
	}
}
