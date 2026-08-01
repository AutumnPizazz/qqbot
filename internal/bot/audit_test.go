package bot

import (
	"path/filepath"
	"testing"
	"time"

	"qqbot/internal/fsutil"
)

func TestAuditQueryPagination(t *testing.T) {
	store := newAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	base := time.Now().Add(-time.Hour)
	// 写入 25 条：群 1 前 20 条 ok，后 5 条 failed；群 2 3 条 unknown
	for i := 0; i < 20; i++ {
		_ = store.append(AuditEntry{
			Time:    base.Add(time.Duration(i) * time.Minute),
			GroupID: 111, Source: "api", Action: "禁言",
			ActionStatus: "ok", Result: "ok",
		})
	}
	for i := 20; i < 25; i++ {
		_ = store.append(AuditEntry{
			Time:    base.Add(time.Duration(i) * time.Minute),
			GroupID: 111, Source: "api", Action: "移出群",
			ActionStatus: "failed", Result: "failed",
		})
	}
	for i := 0; i < 3; i++ {
		_ = store.append(AuditEntry{
			Time:    base.Add(time.Duration(i) * time.Minute),
			GroupID: 222, Source: "manual", Action: "撤回消息",
			ActionStatus: "unknown", Result: "failed",
		})
	}

	// 首页：群 111 全部（倒序：最近的 failed 在前）
	page := store.query(AuditQuery{GroupID: 111, Limit: 10})
	if len(page.Entries) != 10 {
		t.Fatalf("首页应 10 条，实际 %d", len(page.Entries))
	}
	if page.Entries[0].ActionStatus != "failed" || page.Entries[0].Action != "移出群" {
		t.Fatalf("倒序错误: %+v", page.Entries[0])
	}
	if page.NextCursor == 0 {
		t.Fatal("首页应有下一页")
	}
	// 第二页（游标翻页）
	page2 := store.query(AuditQuery{GroupID: 111, Limit: 10, Cursor: page.NextCursor})
	if len(page2.Entries) != 10 {
		t.Fatalf("第二页应 10 条，实际 %d", len(page2.Entries))
	}
	if page2.Entries[0].ActionStatus != "ok" {
		t.Fatalf("第二页应为 ok 记录: %+v", page2.Entries[0])
	}
	// 第三页：剩余 5 条
	page3 := store.query(AuditQuery{GroupID: 111, Limit: 10, Cursor: page2.NextCursor})
	if len(page3.Entries) != 5 {
		t.Fatalf("第三页应 5 条，实际 %d", len(page3.Entries))
	}
	if page3.NextCursor != 0 {
		t.Fatal("末页不应有下一页")
	}
	// 过滤条件
	onlyFailed := store.query(AuditQuery{GroupID: 111, Result: "failed", Limit: 100})
	if len(onlyFailed.Entries) != 5 {
		t.Fatalf("failed 过滤应 5 条，实际 %d", len(onlyFailed.Entries))
	}
	onlyUnknown := store.query(AuditQuery{Result: "unknown", Limit: 100})
	if len(onlyUnknown.Entries) != 3 {
		t.Fatalf("unknown 过滤应 3 条，实际 %d", len(onlyUnknown.Entries))
	}
	bySource := store.query(AuditQuery{Source: "manual", Limit: 100})
	if len(bySource.Entries) != 3 {
		t.Fatalf("source 过滤应 3 条，实际 %d", len(bySource.Entries))
	}
}

func TestAuditLegacyFormatCompatible(t *testing.T) {
	store := newAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	_ = store.append(AuditEntry{
		GroupID: 111, ActorID: 10001, Source: "manual", Action: "禁言", Result: "ok",
	})
	// 旧格式字段缺失时补默认值
	entries := store.list(111, 10)
	if len(entries) != 1 {
		t.Fatal("查询失败")
	}
	e := entries[0]
	if e.ActionStatus != "ok" || e.ActorType != "admin" {
		t.Fatalf("默认值未补齐: %+v", e)
	}
	// 旧审计文件（无新字段）加载兼容
	oldJSON := `[{"time":"2026-01-01T00:00:00Z","group_id":1,"actor_id":2,"source":"manual","action":"踢人","target_id":3,"result":"failed","detail":"权限不足"}]`
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.json")
	_ = fsutil.WriteFileAtomic(path, []byte(oldJSON), 0o644)
	store2 := newAuditStore(path)
	entries2 := store2.list(1, 10)
	if len(entries2) != 1 || entries2[0].Action != "踢人" {
		t.Fatalf("旧格式加载失败: %+v", entries2)
	}
	if entries2[0].ActionStatus != "" {
		t.Fatalf("旧条目 ActionStatus 应为空: %+v", entries2[0])
	}
	if entries2[0].Source != "manual" || entries2[0].Result != "failed" || entries2[0].Detail != "权限不足" {
		t.Fatalf("旧格式字段解析错误: %+v", entries2[0])
	}
}

// list 测试辅助：按群倒序取最近 N 条（生产代码已不使用，仅测试）。
func (s *auditStore) list(groupID int64, limit int) []AuditEntry {
	p := s.query(AuditQuery{GroupID: groupID, Limit: limit})
	return p.Entries
}
