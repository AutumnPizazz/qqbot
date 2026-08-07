package civgo

import (
	"strings"
	"testing"
)

func TestChunkFileHeadingSplit(t *testing.T) {
	content := "# 弓手\n\n弓手是远程单位。\n射程 2 格。\n\n## 升级\n\n升级后攻击力翻倍。\n"
	chunks := ChunkFile("units/archer.md", content, 800, 100)
	if len(chunks) != 2 {
		t.Fatalf("应切出 2 块，got %d", len(chunks))
	}
	if chunks[0].Heading != "弓手" {
		t.Errorf("第 1 块 heading 应为 弓手，got %q", chunks[0].Heading)
	}
	if !strings.Contains(chunks[0].Text, "远程单位") {
		t.Errorf("第 1 块应含正文: %q", chunks[0].Text)
	}
	if chunks[1].Heading != "升级" {
		t.Errorf("第 2 块 heading 应为 升级，got %q", chunks[1].Heading)
	}
	if chunks[0].Index != 0 || chunks[1].Index != 1 {
		t.Errorf("Index 序号错误: %d %d", chunks[0].Index, chunks[1].Index)
	}
	if chunks[0].File != "units/archer.md" {
		t.Errorf("File 应为相对路径: %q", chunks[0].File)
	}
}

func TestChunkFileSizeBoundary(t *testing.T) {
	// 300 个字符的行，chunk_size=200 → 整行成块（不跨行硬切）
	line := strings.Repeat("甲", 300)
	chunks := ChunkFile("a.md", line+"\n", 200, 0)
	if len(chunks) != 1 {
		t.Fatalf("超长单行应整行成块，got %d 块", len(chunks))
	}
	if runeLen(chunks[0].Text) < 300 {
		t.Errorf("块应包含完整长行，got %d rune", runeLen(chunks[0].Text))
	}
}

func TestChunkFileMultiBlock(t *testing.T) {
	// 多行累积超过 chunk_size 时按行封口成多块
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("数据行内容填充")
		sb.WriteString(strings.Repeat("字", 100))
		sb.WriteString("\n")
	}
	chunks := ChunkFile("b.md", sb.String(), 300, 0)
	if len(chunks) < 2 {
		t.Fatalf("应切出多块，got %d", len(chunks))
	}
	for _, c := range chunks {
		if runeLen(c.Text) > 300+110 {
			t.Errorf("块超长: %d rune", runeLen(c.Text))
		}
	}
	// 相邻块 Index 连续
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Index != chunks[i-1].Index+1 {
			t.Errorf("Index 不连续: %d -> %d", chunks[i-1].Index, chunks[i].Index)
		}
	}
}

func TestChunkFileSkipUnsupported(t *testing.T) {
	if got := ChunkFile("data.json", `{"a":1}`, 800, 0); got != nil {
		t.Error("json 应被跳过")
	}
	if got := ChunkFile("IMG.PNG", "x", 800, 0); got != nil {
		t.Error("png 应被跳过（大小写不敏感）")
	}
}

func TestChunkFileEmptyAndBlank(t *testing.T) {
	if got := ChunkFile("empty.md", "", 800, 0); got != nil {
		t.Error("空文件应返回 nil")
	}
	if got := ChunkFile("blank.md", "\n\n   \n", 800, 0); got != nil {
		t.Error("纯空白应返回 nil")
	}
}

func TestChunkFileNoHeading(t *testing.T) {
	content := "第一行\n第二行\n"
	chunks := ChunkFile("plain.txt", content, 800, 0)
	if len(chunks) != 1 {
		t.Fatalf("应切出 1 块，got %d", len(chunks))
	}
	if chunks[0].Heading != "" {
		t.Errorf("无标题文件 heading 应为空，got %q", chunks[0].Heading)
	}
	if !strings.Contains(chunks[0].Text, "第一行") || !strings.Contains(chunks[0].Text, "第二行") {
		t.Errorf("正文缺失: %q", chunks[0].Text)
	}
}

func TestChunkFileCRLF(t *testing.T) {
	content := "# 标题\r\n正文行\r\n"
	chunks := ChunkFile("crlf.md", content, 800, 0)
	if len(chunks) != 1 {
		t.Fatalf("应切出 1 块，got %d", len(chunks))
	}
	if strings.Contains(chunks[0].Text, "\r") {
		t.Errorf("块内不应残留 \\r: %q", chunks[0].Text)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("短文本", 10); got != "短文本" {
		t.Errorf("未超限不应截断: %q", got)
	}
	got := truncateRunes("一二三四五六七八九十", 5)
	if got != "一二三四五…" {
		t.Errorf("截断错误: %q", got)
	}
}

func TestTrimLeadingNonCJK(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"@昵称 弓手怎么升级", "弓手怎么升级"},
		{"  ，。！ 弓手怎么升级", "弓手怎么升级"},
		{"弓手怎么升级", "弓手怎么升级"},
		{"abc123 弓手", "abc123 弓手"},
	}
	for _, tc := range cases {
		if got := trimLeadingNonCJK(tc.in); got != tc.want {
			t.Errorf("trim(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
