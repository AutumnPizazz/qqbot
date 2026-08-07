package civgo

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"
)

// countEmbed 统计嵌入调用次数的 EmbedClient 包装（验证增量重建只重嵌入变化文件）。
type countEmbed struct {
	*EmbedClient
	n *atomic.Int64
}

func (c *countEmbed) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	c.n.Add(int64(len(texts)))
	vecs := make([][]float32, len(texts))
	for i, t := range texts {
		vecs[i] = fakeVec(t)
	}
	return vecs, nil
}

// fakeVec 从文本派生的确定性向量：字符直方图（位置无关，同字符同桶累加）。
func fakeVec(text string) []float32 {
	v := make([]float32, 64)
	for _, r := range text {
		if unicode.IsSpace(r) || r == '#' {
			continue
		}
		v[int(r)%64]++
	}
	return normalize(v)
}

func testIndexer(t *testing.T, docsDir string) (*Indexer, *countEmbed, string) {
	t.Helper()
	cfg := testAIConfig()
	embed := NewEmbedClient(cfg)
	ce := &countEmbed{EmbedClient: embed, n: &atomic.Int64{}}
	idxPath := filepath.Join(t.TempDir(), "index.json")
	ix := NewIndexer(ce, idxPath, RetrievalConfig{
		Mode: "vector", TopK: 3, ChunkSize: 800, MinScore: 0.2,
	})
	return ix, ce, idxPath
}

func writeDoc(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildChangedAndSearch(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "units/archer.md", "# 弓手\n弓手是远程单位，射程 2 格。\n")
	writeDoc(t, dir, "units/knight.md", "# 骑士\n骑士是近战单位，移动力高。\n")
	ix, ce, _ := testIndexer(t, dir)

	sum, err := ix.RebuildChanged(context.Background(), dir)
	if err != nil {
		t.Fatalf("RebuildChanged 失败: %v", err)
	}
	if sum.Added != 2 || sum.Changed != 0 || sum.Chunks != 2 {
		t.Fatalf("首次重建摘要错误: %+v", sum)
	}
	if ce.n.Load() != 2 {
		t.Errorf("首次应嵌入 2 块，got %d", ce.n.Load())
	}

	hits, err := ix.Search(context.Background(), "弓手")
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("应有命中")
	}
	if hits[0].Chunk.File != "units/archer.md" {
		t.Errorf("最相关应命中 archer.md，got %s", hits[0].Chunk.File)
	}

	// 无变化：不重嵌入
	ce.n.Store(0)
	sum2, err := ix.RebuildChanged(context.Background(), dir)
	if err != nil {
		t.Fatalf("第二次重建失败: %v", err)
	}
	if sum2.Added != 0 || sum2.Changed != 0 || sum2.Chunks != 2 {
		t.Fatalf("无变化摘要错误: %+v", sum2)
	}
	if ce.n.Load() != 0 {
		t.Errorf("无变化不应重嵌入，got %d 次", ce.n.Load())
	}

	// 修改一个文件 + 新增一个文件：只嵌入变化部分
	writeDoc(t, dir, "units/archer.md", "# 弓手\n弓手是远程单位，射程 3 格，攻击力 5。\n")
	writeDoc(t, dir, "buildings/barracks.md", "# 兵营\n兵营可以训练步兵。\n")
	ce.n.Store(0)
	sum3, err := ix.RebuildChanged(context.Background(), dir)
	if err != nil {
		t.Fatalf("增量重建失败: %v", err)
	}
	if sum3.Added != 1 || sum3.Changed != 1 || sum3.Chunks != 3 {
		t.Fatalf("增量摘要错误: %+v", sum3)
	}
	if ce.n.Load() != 2 {
		t.Errorf("应只嵌入 2 块（变更+新增），got %d", ce.n.Load())
	}

	// 删除一个文件
	if err := os.Remove(filepath.Join(dir, "units", "knight.md")); err != nil {
		t.Fatal(err)
	}
	sum4, err := ix.RebuildChanged(context.Background(), dir)
	if err != nil {
		t.Fatalf("删除后重建失败: %v", err)
	}
	if sum4.Removed != 1 || sum4.Chunks != 2 {
		t.Fatalf("删除摘要错误: %+v", sum4)
	}
	hits, _ = ix.Search(context.Background(), "骑士")
	for _, h := range hits {
		if strings.Contains(h.Chunk.File, "knight") {
			t.Error("已删除文件不应再被命中")
		}
	}
}

func TestSearchMinScoreFilter(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.md", "# 甲\n内容甲甲甲甲甲甲甲甲。\n")
	writeDoc(t, dir, "b.md", "# 乙\n内容乙乙乙乙乙乙乙乙。\n")
	ix, _, _ := testIndexer(t, dir)
	if _, err := ix.RebuildChanged(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// 高阈值 → 无命中
	ix.mu.Lock()
	ix.minScore = 0.99
	ix.mu.Unlock()
	hits, err := ix.Search(context.Background(), "甲")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("高阈值应过滤全部，got %d", len(hits))
	}
}

func TestSearchEmptyIndex(t *testing.T) {
	dir := t.TempDir()
	ix, _, _ := testIndexer(t, dir)
	hits, err := ix.Search(context.Background(), "任何问题")
	if err != nil {
		t.Fatalf("空索引检索不应报错: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("空索引应无命中，got %d", len(hits))
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.md", "# 甲\n内容甲。\n")
	ix, _, idxPath := testIndexer(t, dir)
	if _, err := ix.RebuildChanged(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	// 新索引器加载持久化文件（查询嵌入也用 fake，验证向量持久化生效）
	cfg2 := testAIConfig()
	embed2 := NewEmbedClient(cfg2)
	ce2 := &countEmbed{EmbedClient: embed2, n: &atomic.Int64{}}
	ix2 := NewIndexer(ce2, idxPath, RetrievalConfig{Mode: "vector", TopK: 3, ChunkSize: 800, MinScore: 0.2})
	ix2.Load()
	if len(ix2.entries) != 1 {
		t.Fatalf("加载后应 1 条，got %d", len(ix2.entries))
	}
	// 向量一致性（VecB64 往返）
	if !vecEqual(ix2.entries[0].Vec, ix.entries[0].Vec) {
		t.Error("向量往返不一致")
	}
	// 加载后无需重新嵌入即可检索（验证向量持久化生效）
	hits, err := ix2.Search(context.Background(), "甲")
	if err != nil || len(hits) == 0 {
		t.Fatalf("加载后检索失败: %v %v", hits, err)
	}
}

func vecEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(float64(a[i]-b[i])) > 1e-6 {
			return false
		}
	}
	return true
}

func TestLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "index.json")
	os.WriteFile(idxPath, []byte("{corrupt"), 0o644)
	ix, _, _ := testIndexer(t, dir)
	ix.path = idxPath
	ix.Load() // 不 panic，空索引
	if len(ix.entries) != 0 {
		t.Error("损坏索引应载入为空")
	}
}

func TestKeywordSearch(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.md", "# 弓手\n弓手射程 2 格，攻击力 5。\n弓手是远程单位。\n")
	writeDoc(t, dir, "b.md", "# 骑士\n骑士近战，弓手克星，移动力高。\n")
	ix, _, _ := testIndexer(t, dir)
	// 用真实分块但不嵌入：直接构造 entries
	chunks := ChunkFile("a.md", "# 弓手\n弓手射程 2 格，攻击力 5。\n弓手是远程单位。\n", 800, 0)
	chunksB := ChunkFile("b.md", "# 骑士\n骑士近战，弓手克星，移动力高。\n", 800, 0)
	ix.mu.Lock()
	ix.entries = []IndexEntry{{Chunk: chunks[0]}, {Chunk: chunksB[0]}}
	ix.mu.Unlock()

	hits := ix.KeywordSearch("弓手 射程", 3)
	if len(hits) != 2 {
		t.Fatalf("应 2 命中，got %d", len(hits))
	}
	if hits[0].Chunk.File != "a.md" {
		t.Errorf("弓手文件应排第一，got %s", hits[0].Chunk.File)
	}
	if hits[0].Score < hits[1].Score {
		t.Errorf("分数应降序: %v vs %v", hits[0].Score, hits[1].Score)
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct{ in string; want []string }{
		{"弓手", []string{"弓手"}},
		{"弓手射程", []string{"弓手", "手射", "射程"}},
		{"archer range", []string{"archer", "range"}},
		{"弓手 range2", []string{"弓手", "range2"}},
		{"", nil},
	}
	for _, tc := range cases {
		got := tokenize(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("tokenize(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestEncodeDecodeVec(t *testing.T) {
	v := []float32{1.5, -2.25, 3.125, 0}
	s := encodeVec(v)
	got, err := decodeVec(s)
	if err != nil {
		t.Fatal(err)
	}
	if !vecEqual(v, got) {
		t.Errorf("编解码不一致: %v -> %v", v, got)
	}
	if _, err := decodeVec("!!bad!!"); err == nil {
		t.Error("坏编码应报错")
	}
}

func TestSearchTopKOrder(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		writeDoc(t, dir, fmt.Sprintf("f%d.md", i), fmt.Sprintf("# 主题%d\n共同内容 主题%d 相关内容。\n", i, i))
	}
	ix, _, _ := testIndexer(t, dir)
	cfg := testAIConfig()
	_ = cfg
	if _, err := ix.RebuildChanged(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	hits, err := ix.Search(context.Background(), "主题5")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 3 {
		t.Errorf("topK=3 应最多 3 条，got %d", len(hits))
	}
	if hits[0].Chunk.File != "f5.md" {
		t.Errorf("主题5 应排第一，got %s", hits[0].Chunk.File)
	}
	// 分数降序
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Error("分数应降序")
		}
	}
}

func TestScanDocsSkipsUnsupported(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.md", "x")
	writeDoc(t, dir, "b.txt", "y")
	writeDoc(t, dir, "c.json", "{}")
	writeDoc(t, dir, ".hidden.md", "z")
	writeDoc(t, dir, "sub/d.md", "w")
	files, err := scanDocs(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.md", "b.txt", "sub/d.md"}
	if len(files) != len(want) {
		t.Fatalf("扫描结果错误: %v", files)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("扫描结果错误: got %v want %v", files, want)
			break
		}
	}
	sort.Strings(files) // 确认已排序
}

func TestRebuildKeywordNoEmbed(t *testing.T) {
	// keyword 模式：索引构建不调用外部嵌入（网关无嵌入模型时的核心保障）
	dir := t.TempDir()
	writeDoc(t, dir, "a.md", "# 弓手\n弓手射程 2 格。\n")
	cfg := testAIConfig()
	ce := &countEmbed{EmbedClient: NewEmbedClient(cfg), n: &atomic.Int64{}}
	ix := NewIndexer(ce, filepath.Join(t.TempDir(), "i.json"), RetrievalConfig{
		Mode: "keyword", TopK: 3, ChunkSize: 800, MinScore: 0.2,
	})
	sum, err := ix.RebuildChanged(context.Background(), dir)
	if err != nil {
		t.Fatalf("keyword 构建失败: %v", err)
	}
	if ce.n.Load() != 0 {
		t.Errorf("keyword 模式不应调用嵌入，got %d", ce.n.Load())
	}
	if sum.Chunks != 1 {
		t.Fatalf("应有 1 块，got %d", sum.Chunks)
	}
	// keyword 检索可用
	hits := ix.KeywordSearch("弓手", 3)
	if len(hits) != 1 || hits[0].Chunk.File != "a.md" {
		t.Errorf("keyword 检索失败: %+v", hits)
	}
	// 持久化往返：keyword 索引（无向量）也能加载
	ix.Save()
	ix2 := NewIndexer(ce, filepath.Join(t.TempDir(), "i2.json"), RetrievalConfig{Mode: "keyword"})
	// 复制保存的文件
	data, _ := os.ReadFile(ix.path)
	os.WriteFile(ix2.path, data, 0o644)
	ix2.Load()
	if len(ix2.entries) != 1 {
		t.Fatalf("keyword 索引加载后应 1 条，got %d", len(ix2.entries))
	}
}

func TestRebuildFailedKeepsOld(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.md", "# 甲\n内容甲。\n")
	ix, ce, _ := testIndexer(t, dir)
	if _, err := ix.RebuildChanged(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	before := len(ix.entries)

	// 让嵌入失败：新文件触发，但 mock 掉 embed 后失败
	writeDoc(t, dir, "b.md", "# 乙\n内容乙。\n")
	failing := &failingEmbed{EmbedClient: ce.EmbedClient}
	ix.embed = failing
	if _, err := ix.RebuildChanged(context.Background(), dir); err == nil {
		t.Fatal("嵌入失败应报错")
	}
	if len(ix.entries) != before {
		t.Errorf("失败后应保留旧索引，got %d want %d", len(ix.entries), before)
	}
}

type failingEmbed struct{ *EmbedClient }

func (f *failingEmbed) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, fmt.Errorf("mock 嵌入失败")
}
