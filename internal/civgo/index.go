package civgo

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// indexVersion 索引文件格式版本（不兼容时全量重建）。
const indexVersion = 1

// IndexEntry 一条索引：分块文本 + 归一化向量。
type IndexEntry struct {
	Chunk  Chunk     `json:"chunk"`
	Vec    []float32 `json:"-"`      // 内存态（L2 归一化）
	VecB64 string    `json:"vec_b64"` // 持久化：Float32bits 打包后 base64
}

// IndexFile 磁盘持久化格式。
type IndexFile struct {
	Version    int               `json:"version"`
	Entries    []IndexEntry      `json:"entries"`
	FileHashes map[string]string `json:"file_hashes"` // relPath → sha256(内容)
	BuiltAt    time.Time         `json:"built_at"`
}

// Hit 一次检索命中。
type Hit struct {
	Chunk Chunk
	Score float64
}

// Summary 一次索引重建的变更摘要。
type Summary struct {
	Added   int `json:"added"`
	Changed int `json:"changed"`
	Removed int `json:"removed"`
	Chunks  int `json:"chunks"`
}

func (s Summary) String() string {
	return fmt.Sprintf("files+%d/~%d/-%d chunks=%d", s.Added, s.Changed, s.Removed, s.Chunks)
}

// Embedder 嵌入接口（Indexer 的依赖抽象，测试可注入 fake）。
type Embedder interface {
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

// Indexer 轻量向量索引：余弦相似度检索 + JSON 持久化 + 内容哈希增量重建。
// 零第三方依赖（文档量级数千块，暴力线性检索毫秒级）。
type Indexer struct {
	mu        sync.RWMutex
	entries   []IndexEntry
	hashes    map[string]string
	embed     Embedder
	mode      string // vector（默认）| keyword（嵌入不可用时的降级）
	topK      int
	minScore  float64
	chunkSize int
	path      string
}

// NewIndexer 创建索引器。
func NewIndexer(embed Embedder, path string, cfg RetrievalConfig) *Indexer {
	return &Indexer{
		embed:     embed,
		mode:      cfg.Mode,
		topK:      cfg.TopK,
		minScore:  cfg.MinScore,
		chunkSize: cfg.ChunkSize,
		path:      path,
		hashes:    map[string]string{},
	}
}

// Mode 返回当前检索模式。
func (ix *Indexer) Mode() string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.mode
}

// SetMode 切换检索模式（嵌入自检失败降级 keyword / 恢复 vector）。
func (ix *Indexer) SetMode(m string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if m != "vector" && m != "keyword" {
		return
	}
	if ix.mode != m {
		ix.mode = m
		slog.Info("civgo 检索模式切换", "mode", m)
	}
}

// Load 从磁盘载入索引。文件缺失/损坏 → 日志 + 空索引（由首次同步全量重建）。
func (ix *Indexer) Load() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	data, err := os.ReadFile(ix.path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("civgo 索引读取失败（将重建）", "err", err)
		}
		return
	}
	var f IndexFile
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("civgo 索引解析失败（将重建）", "err", err)
		return
	}
	if f.Version != indexVersion {
		slog.Warn("civgo 索引版本不兼容（将重建）", "version", f.Version)
		return
	}
	entries := make([]IndexEntry, 0, len(f.Entries))
	skipped := 0
	for _, e := range f.Entries {
		vec, err := decodeVec(e.VecB64)
		if err != nil {
			skipped++
			continue
		}
		e.Vec = vec
		entries = append(entries, e)
	}
	ix.entries = entries
	if f.FileHashes != nil {
		ix.hashes = f.FileHashes
	} else {
		ix.hashes = map[string]string{}
	}
	slog.Info("civgo 索引已载入", "chunks", len(entries), "skipped", skipped)
}

// Save 原子写索引（临时文件 + rename）。向量以 base64 编码落盘。
func (ix *Indexer) Save() error {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	entries := make([]IndexEntry, len(ix.entries))
	for i, e := range ix.entries {
		e.VecB64 = encodeVec(e.Vec)
		entries[i] = e
	}
	f := IndexFile{
		Version:    indexVersion,
		Entries:    entries,
		FileHashes: ix.hashes,
		BuiltAt:    time.Now(),
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ix.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(ix.path), ".index-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, ix.path)
}

// Search 检索与 query 最相关的 top-k 块。
// vector 模式：嵌入查询 → 余弦相似度（已归一化，点积）→ 过滤 min_score；
// keyword 模式：词频粗筛（嵌入不可用时的降级）。
func (ix *Indexer) Search(ctx context.Context, query string) ([]Hit, error) {
	if ix.Mode() == "keyword" {
		return ix.KeywordSearch(query, ix.topK), nil
	}
	vecs, err := ix.embed.EmbedTexts(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	q := normalize(vecs[0])
	if q == nil {
		return nil, fmt.Errorf("查询向量为空")
	}

	ix.mu.RLock()
	defer ix.mu.RUnlock()
	scored := make([]Hit, 0, len(ix.entries))
	for _, e := range ix.entries {
		score := dot(q, e.Vec)
		if score >= ix.minScore {
			scored = append(scored, Hit{Chunk: e.Chunk, Score: score})
		}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if len(scored) > ix.topK {
		scored = scored[:ix.topK]
	}
	return scored, nil
}

// KeywordSearch 关键词降级检索：字母数字词 + CJK 二元分词，词频加权，
// 文件名/标题命中加权；取 top-k（不应用向量 min_score，尺度不同）。
func (ix *Indexer) KeywordSearch(query string, topK int) []Hit {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	scored := make([]Hit, 0, len(ix.entries))
	for _, e := range ix.entries {
		score := 0
		text := strings.ToLower(e.Chunk.Text)
		name := strings.ToLower(e.Chunk.File + " " + e.Chunk.Heading)
		for _, tok := range tokens {
			if n := strings.Count(text, tok); n > 0 {
				score += n
				if strings.Contains(name, tok) {
					score += 3
				}
			}
		}
		if score > 0 {
			scored = append(scored, Hit{Chunk: e.Chunk, Score: float64(score)})
		}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if len(scored) > topK {
		scored = scored[:topK]
	}
	return scored
}

// RebuildChanged 增量重建：扫描 docsDir，按内容哈希只重嵌入变化文件。
// 任一步失败保留旧索引（整体原子），返回错误供下次重试。
func (ix *Indexer) RebuildChanged(ctx context.Context, docsDir string) (Summary, error) {
	var sum Summary
	files, err := scanDocs(docsDir)
	if err != nil {
		return sum, err
	}
	newHashes := map[string]string{}
	toEmbed := map[string][]Chunk{} // 变化/新增文件的分块
	order := []string{}

	for _, f := range files {
		content, err := os.ReadFile(filepath.Join(docsDir, f))
		if err != nil {
			slog.Warn("civgo 读取文档失败（跳过）", "file", f, "err", err)
			continue
		}
		hash := sha256Hex(content)
		newHashes[f] = hash
		if _, ok := ix.hashes[f]; !ok {
			sum.Added++
		} else if ix.hashes[f] != hash {
			sum.Changed++
		} else {
			continue // 未变化，复用旧向量
		}
		chunks := ChunkFile(f, string(content), ix.chunkSize, 100)
		if len(chunks) == 0 {
			slog.Warn("civgo 文档无有效内容（跳过）", "file", f)
			continue
		}
		toEmbed[f] = chunks
		order = append(order, f)
	}
	// 本地已删除的文件
	for f := range ix.hashes {
		if _, ok := newHashes[f]; !ok {
			sum.Removed++
		}
	}

	// 变化/新增文件全部嵌入成功才算成功（失败保留旧索引）
	newVecs := map[string][][]float32{}
	for _, f := range order {
		vecs, err := ix.embed.EmbedTexts(ctx, textsOf(toEmbed[f]))
		if err != nil {
			return sum, fmt.Errorf("嵌入 %s 失败: %w", f, err)
		}
		newVecs[f] = vecs
	}

	// 组装新条目：未变文件保留旧条目；变化/新增用新条目；已删除文件丢弃
	changed := map[string]bool{}
	for _, f := range order {
		changed[f] = true
	}
	entries := make([]IndexEntry, 0, len(ix.entries)+len(order))
	for _, e := range ix.entries {
		if _, ok := newHashes[e.Chunk.File]; !ok {
			continue // 文件已删除
		}
		if !changed[e.Chunk.File] {
			entries = append(entries, e)
		}
	}
	for _, f := range order {
		for i, c := range toEmbed[f] {
			entries = append(entries, IndexEntry{Chunk: c, Vec: newVecs[f][i]})
		}
	}

	ix.mu.Lock()
	ix.entries = entries
	ix.hashes = newHashes
	ix.mu.Unlock()

	if len(files) == 0 {
		slog.Warn("civgo 文档目录为空或不存在（docs_path 可能配置错误）", "dir", docsDir)
	}
	sum.Chunks = len(entries)
	if err := ix.Save(); err != nil {
		return sum, err
	}
	return sum, nil
}

// textsOf 提取块文本列表（嵌入入参）。
func textsOf(chunks []Chunk) []string {
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	return texts
}

// RebuildAll 全量重建索引（兜底：清空后走增量流程）。
func (ix *Indexer) RebuildAll(ctx context.Context, docsDir string) (Summary, error) {
	ix.mu.Lock()
	ix.entries = nil
	ix.hashes = map[string]string{}
	ix.mu.Unlock()
	return ix.RebuildChanged(ctx, docsDir)
}

// scanDocs 递归扫描目录下的支持类型文件，返回相对路径列表（排除隐藏文件）。
func scanDocs(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !SupportedExt(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil // 目录尚未存在（首次同步前）
	}
	sort.Strings(out)
	return out, err
}

// ---- 向量工具 ----

// normalize L2 归一化；零向量返回 nil。
func normalize(v []float32) []float32 {
	if len(v) == 0 {
		return nil
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return nil
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func dot(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func encodeVec(v []float32) string {
	buf := make([]byte, len(v)*4)
	for i, x := range v {
		u := math.Float32bits(x)
		buf[i*4] = byte(u)
		buf[i*4+1] = byte(u >> 8)
		buf[i*4+2] = byte(u >> 16)
		buf[i*4+3] = byte(u >> 24)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func decodeVec(s string) ([]float32, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw)%4 != 0 {
		return nil, fmt.Errorf("向量编码损坏")
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		u := uint32(raw[i*4]) | uint32(raw[i*4+1])<<8 | uint32(raw[i*4+2])<<16 | uint32(raw[i*4+3])<<24
		out[i] = math.Float32frombits(u)
	}
	return out, nil
}

// ---- 关键词分词（keyword 降级检索用） ----

// tokenize 中文混合分词：字母数字串整体成词；连续 CJK 汉字按二元切分。
// 类型切换（字母数字 ↔ CJK ↔ 其他）时冲刷当前缓冲区。
func tokenize(s string) []string {
	lower := strings.ToLower(s)
	var tokens []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, string(word))
			word = nil
		}
	}
	flushCJK := func() {
		if len(cjk) > 0 {
			for i := 0; i+1 < len(cjk); i++ {
				tokens = append(tokens, string(cjk[i:i+2]))
			}
			cjk = nil
		}
	}
	for _, r := range lower {
		switch {
		case isCJK(r):
			if len(word) > 0 {
				flushWord()
			}
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if len(cjk) > 0 {
				flushCJK()
			}
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return tokens
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

// sha256Hex 计算内容哈希（增量重建的文件指纹）。
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
