package civgo

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// docmapVersion 文档地图格式版本（不兼容时全量重建）。
const docmapVersion = 1

// DocHeading 文档内一个 markdown 标题（大纲条目）。
type DocHeading struct {
	Line  int    `json:"line"`  // 行号（1 起）
	Level int    `json:"level"` // # 数量（1~6）
	Text  string `json:"text"`  // 去 # 后的标题文本
}

// DocmapFile 单个文档的元数据。
type DocmapFile struct {
	Bytes     int64        `json:"bytes"`
	Lines     int          `json:"lines"`
	IndexFile bool         `json:"index_file"` // 索引/阅读顺序类文档（工具描述引导优先阅读）
	Headings  []DocHeading `json:"headings"`
	Hash      string       `json:"hash,omitempty"` // 内容 sha256（增量重建指纹）
}

// Docmap 文档地图：docs 目录的文件清单 + 大纲，供 AI 低成本定位后精准阅读，
// 替代旧向量索引。文件 → 元数据（相对 docs_path 路径）。
type Docmap struct {
	Version int                   `json:"version"`
	BuiltAt time.Time             `json:"built_at"`
	Files   map[string]DocmapFile `json:"files"`
}

// docHeadingRe 匹配 markdown 标题行（# 至 ######）。
// （命名区别于旧 chunk.go 的 headingRe，待旧分块器删除后合并。）
var docHeadingRe = regexp.MustCompile(`^#{1,6}\s+`)

// DocmapStore 文档地图的原子读写（同步器更新，工具集只读）。
type DocmapStore struct {
	mu   sync.RWMutex
	m    *Docmap
	path string
}

// NewDocmapStore 载入 docmap.json；缺失/损坏返回空地图（首次同步后填充），不阻断。
func NewDocmapStore(path string) *DocmapStore {
	s := &DocmapStore{
		path: path,
		m:    &Docmap{Version: docmapVersion, Files: map[string]DocmapFile{}},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("civgo docmap 读取失败（将重建）", "err", err)
		}
		return s
	}
	var m Docmap
	if err := json.Unmarshal(data, &m); err != nil {
		slog.Warn("civgo docmap 解析失败（将重建）", "err", err)
		return s
	}
	if m.Version != docmapVersion || m.Files == nil {
		return s
	}
	s.m = &m
	slog.Info("civgo docmap 已载入", "files", len(m.Files))
	return s
}

// Get 返回当前文档地图（只读，勿改）。
func (s *DocmapStore) Get() *Docmap {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m
}

// Replace 原子替换并落盘（落盘失败仅日志，内存态仍生效）。
func (s *DocmapStore) Replace(m *Docmap) {
	s.mu.Lock()
	s.m = m
	s.mu.Unlock()
	if err := saveDocmap(s.path, m); err != nil {
		slog.Warn("civgo docmap 落盘失败", "err", err)
	}
}

// BuildDocmap 扫描 docsDir 生成文档地图：按内容哈希增量复用旧条目
// （未变化文件保留旧大纲，仅变化/新增文件重提大纲）。
// 返回新地图并落盘；目录不存在返回空地图不报错（首次同步前）。
func BuildDocmap(docsDir, path string, old *Docmap) (*Docmap, error) {
	files, err := scanDocs(docsDir)
	if err != nil {
		return nil, err
	}
	m := &Docmap{Version: docmapVersion, BuiltAt: time.Now(), Files: map[string]DocmapFile{}}
	for _, f := range files {
		content, err := os.ReadFile(filepath.Join(docsDir, f))
		if err != nil {
			slog.Warn("civgo 读取文档失败（跳过）", "file", f, "err", err)
			continue
		}
		hash := sha256Hex(content)
		if old != nil {
			if prev, ok := old.Files[f]; ok && prev.Hash == hash {
				m.Files[f] = prev // 未变化，复用旧条目
				continue
			}
		}
		headings, lines := extractHeadings(string(content))
		m.Files[f] = DocmapFile{
			Bytes:     int64(len(content)),
			Lines:     lines,
			IndexFile: isIndexFile(f),
			Headings:  headings,
			Hash:      hash,
		}
	}
	if err := saveDocmap(path, m); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		slog.Warn("civgo 文档目录为空或不存在（docs_path 可能配置错误）", "dir", docsDir)
	}
	return m, nil
}

// extractHeadings 提取文档大纲：markdown 标题列表（行号 1 起）与总行数。
// 围栏代码块（```）内的 # 行不计入标题；文件末尾换行符不额外计行。
func extractHeadings(content string) ([]DocHeading, int) {
	rawLines := strings.Split(content, "\n")
	if n := len(rawLines); n > 0 && rawLines[n-1] == "" {
		rawLines = rawLines[:n-1] // 末尾换行符产生的空串不计行
	}
	var out []DocHeading
	inCode := false
	for i, raw := range rawLines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if m := docHeadingRe.FindString(line); m != "" {
			out = append(out, DocHeading{
				Line:  i + 1,
				Level: len(strings.TrimRight(m, " \t")),
				Text:  strings.TrimSpace(strings.TrimPrefix(line, m)),
			})
		}
	}
	return out, len(rawLines)
}

// saveDocmap 原子写 docmap.json（临时文件 + rename）。
func saveDocmap(path string, m *Docmap) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".docmap-*.tmp")
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
	return os.Rename(tmpName, path)
}
