package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"qqbot/internal/config"
)

// ConfigService 是配置的唯一入口：加载 control.json、事务化更新、
// 历史快照与恢复，并把生效配置发布为无锁快照。
//
// 事务顺序（设计文档 8 节）：
//
//	锁 → revision 校验 → 候选副本 → mutate → 校验 → 临时文件 fsync →
//	原子 rename → 内存切换 → 生效配置快照 → 通知订阅者 → 解锁
//
// 校验失败或写盘失败都不会改变内存与磁盘。
type ConfigService struct {
	mu   sync.Mutex
	path string
	keys *MasterKey
	cur  *Control
	eff  atomic.Pointer[config.Config]
	subs map[int64]func()
	seq  int64
}

// Open 打开数据目录下的 control.json。
//   - control.json 不存在：返回未初始化的空服务（Initialized()==false）。
//   - control.json 损坏：尝试 .bak；仍失败则返回错误，不静默回退为空配置。
//   - 加载成功后立即构建生效配置并校验（失败返回错误）。
func Open(dataDir string, keys *MasterKey) (*ConfigService, error) {
	path := filepath.Join(dataDir, "control.json")
	s := &ConfigService{path: path, keys: keys, subs: map[int64]func(){}}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.cur = emptyControl()
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 control.json 失败: %w", err)
	}
	c, derr := DecodeControl(raw)
	if derr != nil {
		// v1 → v2 自动迁移（设计文档 RULE_ENGINE_DESIGN §8）
		if se := schemaVersionOf(derr); se == 1 {
			c, err = migrateAndWriteBack(s, raw)
			if err != nil {
				return nil, err
			}
		} else {
			slog.Warn("control.json 解析失败，尝试 .bak 恢复", "err", derr)
			raw, err = os.ReadFile(path + ".bak")
			if err != nil {
				return nil, fmt.Errorf("control.json 损坏且 .bak 不可用: %w；%v", err, derr)
			}
			c, derr = DecodeControl(raw)
			if derr != nil {
				return nil, fmt.Errorf("control.json 与 .bak 均损坏: %w", derr)
			}
		}
	}
	if err := s.loadControl(c); err != nil {
		// 加载失败（如密文 AAD 版本不匹配）→ 尝试重加密修复后重载
		if repaired, ok := s.repairLegacySecrets(c); ok {
			c = repaired
			err = s.loadControl(c)
		}
		if err != nil {
			return nil, fmt.Errorf("control.json 内容校验失败: %w", err)
		}
	} else if repaired, ok := s.repairLegacySecrets(c); ok {
		// 加载成功但存在 v1 遗留密文（Decrypt 兼容读）→ 主动重加密写盘
		c = repaired
		s.cur = c
		if data, merr := EncodeControl(repaired); merr == nil {
			if werr := writeFileAtomic(s.path, data); werr == nil {
				_ = writeFileAtomic(s.path+".bak", data) // best effort
			}
		}
		slog.Warn("已修复 v1 遗留密文（重加密为当前 schema 版本）", "revision", repaired.Revision)
	}
	slog.Info("已加载 control.json", "revision", c.Revision, "groups", len(c.Groups))
	return s, nil
}

// repairLegacySecrets 检测并用 v1 AAD 解密、当前版本 AAD 重加密敏感字段。
// 返回 (修复后的配置副本, 是否有旧密文被修复)。无法解密的密文保持原样（由调用方报错）。
func (s *ConfigService) repairLegacySecrets(c *Control) (*Control, bool) {
	cp := deepCopyControl(c)
	fixed := false
	fix := func(v **EncryptedValue, field string) {
		if *v == nil {
			return
		}
		plain, legacy, err := s.keys.DecryptCompat(*v, field)
		if err != nil || !legacy {
			return // 非旧密文或无法解密：保持原样
		}
		enc, err := s.keys.Encrypt(plain, field)
		if err != nil {
			return
		}
		*v = enc
		fixed = true
	}
	fix(&cp.System.OneBot.AccessToken, fieldOneBotToken)
	fix(&cp.System.NapCat.WebUIToken, fieldNapCatToken)
	// 历史快照同样修复（恢复功能依赖可解密的快照）
	for i := range cp.History {
		h := &cp.History[i]
		if h.Config == nil {
			continue
		}
		fix(&h.Config.System.OneBot.AccessToken, fieldOneBotToken)
		fix(&h.Config.System.NapCat.WebUIToken, fieldNapCatToken)
		h.Hash = controlHash(h.Config) // 密文变化后摘要重算
	}
	return cp, fixed
}

// loadControl 校验并装载规范化配置（持有锁）。
func (s *ConfigService) loadControl(c *Control) error {
	eff, err := s.buildEffective(c)
	if err != nil {
		return fmt.Errorf("control.json 内容校验失败: %w", err)
	}
	s.cur = c
	s.eff.Store(eff)
	return nil
}

// schemaVersionOf 从解码错误中提取 schema 版本（v1 自动迁移用）。
func schemaVersionOf(err error) int {
	var se *SchemaError
	if errors.As(err, &se) {
		return se.Version
	}
	return 0
}

// migrateAndWriteBack 读取 v1 control.json → 转换为 v2 → 原子写回（含 .bak）。
// 返回迁移后的 v2 配置；转换或写盘失败均返回错误（原文件不受损）。
func migrateAndWriteBack(s *ConfigService, raw []byte) (*Control, error) {
	v1, err := decodeV1Control(raw)
	if err != nil {
		return nil, fmt.Errorf("control.json 为 schema v1 但解析失败: %w", err)
	}
	c, err := migrateV1ToV2(v1)
	if err != nil {
		return nil, err
	}
	c.SchemaVersion = SchemaVersion
	// v1 密文按 v1 AAD 解密后重加密为当前版本（AAD 含 schema 版本号）
	if repaired, ok := s.repairLegacySecrets(c); ok {
		c = repaired
	}
	data, err := EncodeControl(c)
	if err != nil {
		return nil, fmt.Errorf("序列化迁移后配置失败: %w", err)
	}
	if err := writeFileAtomic(s.path, data); err != nil {
		return nil, fmt.Errorf("迁移写盘失败（v1 文件未改动）: %w", err)
	}
	if err := writeFileAtomic(s.path+".bak", data); err != nil {
		slog.Warn("同步 control.json.bak 失败", "err", err)
	}
	slog.Info("control.json 已从 schema v1 自动迁移到 v2（规则引擎）",
		"revision", c.Revision, "groups", len(c.Groups))
	return c, nil
}

// emptyControl 返回未初始化的配置（schema 版本与 revision 0）。
func emptyControl() *Control {
	return &Control{
		SchemaVersion: SchemaVersion,
		UpdatedAt:     time.Now(),
		System: SystemConfig{
			OneBot: OneBotConfig{APITimeoutMs: 5000},
		},
	}
}

// Initialized 判断是否已有业务配置（供 setup 生命周期使用）。
func (s *ConfigService) Initialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil && s.cur.Revision > 0
}

// Revision 返回当前配置版本。
func (s *ConfigService) Revision() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.Revision
}

// Current 返回当前规范化配置的深拷贝（含历史，供页面渲染）。
func (s *ConfigService) Current() *Control {
	s.mu.Lock()
	defer s.mu.Unlock()
	return deepCopyControl(s.cur)
}

// Effective 返回当前生效配置快照（Bot 事件管线无锁读取）。
func (s *ConfigService) Effective() *config.Config {
	return s.eff.Load()
}

// Subscribe 注册配置变更通知，返回取消函数。
// 回调在 Update 成功提交后、锁外调用，不得长时间阻塞。
func (s *ConfigService) Subscribe(fn func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := s.seq
	s.subs[id] = fn
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// Update 事务化修改配置。
//   - expectedRev 与当前 revision 不一致返回 ErrRevisionConflict（409）。
//   - mutate 在深拷贝的候选上执行；返回 nil 后进入校验与提交。
//   - 校验失败返回 *ValidationError（422），候选直接丢弃。
//   - 写盘失败返回错误，内存与磁盘均保持原值。
//
// actor 与 summary 写入历史条目。返回提交后的新配置（深拷贝）。
func (s *ConfigService) Update(actor string, expectedRev int64, mutate func(*Control) error, summary string) (*Control, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRev != s.cur.Revision {
		return nil, ErrRevisionConflict
	}
	candidate := deepCopyControl(s.cur)
	candidate.History = nil // 候选在提交时才生成历史
	if err := mutate(candidate); err != nil {
		return nil, err
	}
	if err := s.commitLocked(actor, candidate, summary); err != nil {
		return nil, err
	}
	return deepCopyControl(s.cur), nil
}

// Restore 把配置恢复到历史 revision（生成新 revision，不覆盖历史）。
func (s *ConfigService) Restore(actor string, rev int64) (*Control, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var snap *Control
	for i := range s.cur.History {
		if s.cur.History[i].Revision == rev {
			snap = s.cur.History[i].Config
			break
		}
	}
	if snap == nil {
		return nil, fmt.Errorf("历史 revision %d 不存在", rev)
	}
	candidate := deepCopyControl(snap)
	candidate.History = nil
	if err := s.commitLocked(actor, candidate, fmt.Sprintf("恢复到历史 revision %d", rev)); err != nil {
		return nil, err
	}
	return deepCopyControl(s.cur), nil
}

// commitLocked 在锁内完成校验、历史生成与原子写盘（调用方持锁）。
func (s *ConfigService) commitLocked(actor string, candidate *Control, summary string) error {
	// 1. 构造候选生效配置（解密 + NormalizeAndValidate）
	eff, err := s.buildEffective(candidate)
	if err != nil {
		return err // 校验失败：候选丢弃，内存/磁盘不变
	}

	// 2. 生成新 revision 与历史快照
	candidate.Revision = s.cur.Revision + 1
	candidate.UpdatedAt = time.Now()
	hash := controlHash(candidate)
	snap := deepCopyControl(candidate)
	snap.History = nil
	s.cur.History = append(s.cur.History, HistoryEntry{
		Revision: candidate.Revision,
		Time:     candidate.UpdatedAt,
		Actor:    actor,
		Summary:  summary,
		Hash:     hash,
		Config:   snap,
	})
	if n := len(s.cur.History); n > maxHistory {
		s.cur.History = append([]HistoryEntry(nil), s.cur.History[n-maxHistory:]...)
	}
	candidate.History = s.cur.History

	// 3. 原子写盘（0600 + fsync + rename），成功后同步 .bak（最新成功版本）
	data, err := EncodeControl(candidate)
	if err != nil {
		return fmt.Errorf("序列化 control.json 失败: %w", err)
	}
	if err := writeFileAtomic(s.path, data); err != nil {
		return fmt.Errorf("写盘 control.json 失败: %w", err)
	}
	// .bak 是冗余恢复来源（主文件损坏时回退），失败不阻断提交
	if err := writeFileAtomic(s.path+".bak", data); err != nil {
		slog.Warn("同步 control.json.bak 失败", "err", err)
	}

	// 4. 写盘成功后才切换内存
	old := s.cur
	s.cur = candidate
	s.eff.Store(eff)
	subs := make([]func(), 0, len(s.subs))
	for _, fn := range s.subs {
		subs = append(subs, fn)
	}
	slog.Info("配置已提交", "revision", candidate.Revision, "actor", actor, "summary", summary)
	if old.Revision == 0 {
		slog.Info("配置已初始化：业务生命周期可启动")
	}

	// 5. 锁外通知订阅者
	for _, fn := range subs {
		go fn()
	}
	return nil
}

// controlHash 计算配置内容摘要（不含 history，避免历史变化导致摘要漂移）。
func controlHash(c *Control) string {
	snap := deepCopyControl(c)
	snap.History = nil
	data, err := json.Marshal(snap)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// deepCopyControl 深拷贝 Control（marshal/unmarshal，字段均为值类型/切片）。
func deepCopyControl(c *Control) *Control {
	if c == nil {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		// 仅由不可序列化字段触发，理论不可达；退回浅拷贝保证不 panic
		cp := *c
		return &cp
	}
	var out Control
	if err := json.Unmarshal(data, &out); err != nil {
		cp := *c
		return &cp
	}
	return &out
}
