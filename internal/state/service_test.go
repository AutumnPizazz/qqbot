package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qqbot/internal/rules"
)

// initService 构造一个已初始化的 Service：空服务 + 一次基础提交。
func initService(t *testing.T) (*ConfigService, string) {
	t.Helper()
	dir := t.TempDir()
	keys := NewTestMasterKey()
	svc, err := Open(dir, keys)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	_, err = svc.Update("admin", 0, func(c *Control) error {
		c.System.BotName = "测试机器人"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
		c.Groups = append(c.Groups, GroupConfig{GroupID: 123456789, Enabled: true})
		return nil
	}, "初始化")
	if err != nil {
		t.Fatalf("初始化提交失败: %v", err)
	}
	return svc, dir
}

func TestOpenUninitialized(t *testing.T) {
	dir := t.TempDir()
	svc, err := Open(dir, NewTestMasterKey())
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if svc.Initialized() {
		t.Fatal("空目录不应视为已初始化")
	}
	if svc.Revision() != 0 {
		t.Fatalf("初始 revision 应为 0，实际 %d", svc.Revision())
	}
	if _, err := os.Stat(filepath.Join(dir, "control.json")); !os.IsNotExist(err) {
		t.Fatal("未初始化时不应写盘")
	}
}

func TestUpdateCommitsAtomically(t *testing.T) {
	svc, dir := initService(t)
	if svc.Revision() != 1 {
		t.Fatalf("revision 应为 1，实际 %d", svc.Revision())
	}
	_, err := svc.Update("admin", 1, func(c *Control) error {
		c.System.BotName = "改名机器人"
		c.Groups[0].Remark = "测试群"
		return nil
	}, "改名")
	if err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	if svc.Revision() != 2 {
		t.Fatalf("revision 应为 2，实际 %d", svc.Revision())
	}
	// 生效配置即时切换
	eff := svc.Effective()
	if eff.Bot.Name != "改名机器人" {
		t.Fatalf("生效配置未切换: %q", eff.Bot.Name)
	}
	// 磁盘内容为新版本
	data, err := os.ReadFile(filepath.Join(dir, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision != 2 || c.System.BotName != "改名机器人" {
		t.Fatal("磁盘配置与内存不一致")
	}
	// 历史记录：包含 revision 1 的快照
	if len(c.History) != 2 {
		t.Fatalf("历史应有 2 条，实际 %d", len(c.History))
	}
	if c.History[1].Revision != 2 || c.History[1].Summary != "改名" || c.History[1].Actor != "admin" {
		t.Fatalf("历史元信息错误: %+v", c.History[1])
	}
	if c.History[1].Config == nil || c.History[1].Config.System.BotName != "改名机器人" {
		t.Fatal("历史快照内容错误")
	}
	if c.History[1].Hash == "" {
		t.Fatal("历史缺少配置摘要哈希")
	}
}

func TestUpdateRevisionConflict(t *testing.T) {
	svc, _ := initService(t)
	_, err := svc.Update("admin", 99, func(c *Control) error { return nil }, "x")
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("应返回 ErrRevisionConflict，实际 %v", err)
	}
	if svc.Revision() != 1 {
		t.Fatal("冲突更新不得改变 revision")
	}
}

func TestUpdateValidationFailureKeepsState(t *testing.T) {
	svc, dir := initService(t)
	before, err := os.ReadFile(filepath.Join(dir, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 非法值：规则参数越界（刷屏窗口 1 秒 < 3）
	_, err = svc.Update("admin", 1, func(c *Control) error {
		c.Groups[0].Rules = []rules.Rule{{
			ID: "t1", Name: "刷屏", Enabled: true, Event: rules.EventMessage,
			When: []rules.Condition{{
				Type: "flood",
				Params: json.RawMessage(`{"window_sec": 1, "max_count": 8}`),
			}},
			Then: []rules.Action{{Type: "mute", Params: json.RawMessage(`{"minutes": 10}`)}},
			Break: true,
		}}
		return nil
	}, "坏配置")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("应返回 ValidationError，实际 %v", err)
	}
	if _, ok := ve.Fields["groups.0.rules.0.when.0.params"]; !ok {
		t.Fatalf("缺少字段错误: %v", ve.Fields)
	}
	// 内存不变
	if svc.Revision() != 1 {
		t.Fatal("校验失败后 revision 不得变化")
	}
	eff := svc.Effective()
	if len(eff.Groups[0].Rules) != 0 {
		t.Fatal("校验失败后生效配置不得变化")
	}
	// 磁盘不变
	after, err := os.ReadFile(filepath.Join(dir, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("校验失败后磁盘内容发生变化")
	}
	// mutate 中显式返回错误也保持状态
	_, err = svc.Update("admin", 1, func(c *Control) error {
		return errors.New("mutate 内部错误")
	}, "x")
	if err == nil || svc.Revision() != 1 {
		t.Fatal("mutate 错误未正确传播")
	}
}

func TestUpdateWriteFailureKeepsState(t *testing.T) {
	svc, dir := initService(t)
	// 用同名目录占位，使 rename 失败（模拟写盘失败）
	controlPath := filepath.Join(dir, "control.json")
	if err := os.Remove(controlPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(controlPath, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Update("admin", 1, func(c *Control) error {
		c.System.BotName = "不应落盘"
		return nil
	}, "写盘失败场景")
	if err == nil {
		t.Fatal("写盘失败应返回错误")
	}
	if svc.Revision() != 1 {
		t.Fatal("写盘失败后内存 revision 不得变化")
	}
	if svc.Effective().Bot.Name == "不应落盘" {
		t.Fatal("写盘失败后生效配置不得切换")
	}
}

func TestHistoryCap(t *testing.T) {
	svc, _ := initService(t)
	for i := 0; i < maxHistory+5; i++ {
		rev := svc.Revision()
		if _, err := svc.Update("admin", rev, func(c *Control) error {
			c.System.BotName = "r" + string(rune('a'+i%26))
			return nil
		}, "批量"); err != nil {
			t.Fatalf("第 %d 次更新失败: %v", i, err)
		}
	}
	cur := svc.Current()
	if len(cur.History) != maxHistory {
		t.Fatalf("历史应裁剪到 %d 条，实际 %d", maxHistory, len(cur.History))
	}
}

func TestRestoreFromHistory(t *testing.T) {
	svc, _ := initService(t)
	// 第二次修改
	if _, err := svc.Update("admin", 1, func(c *Control) error {
		c.System.BotName = "V2"
		return nil
	}, "v2"); err != nil {
		t.Fatal(err)
	}
	// 恢复到 revision 1
	restored, err := svc.Restore("admin", 1)
	if err != nil {
		t.Fatalf("Restore 失败: %v", err)
	}
	if restored.System.BotName != "测试机器人" {
		t.Fatalf("恢复内容错误: %q", restored.System.BotName)
	}
	if svc.Revision() != 3 {
		t.Fatalf("恢复应产生新 revision 3，实际 %d", svc.Revision())
	}
	// 恢复不删除历史
	if len(svc.Current().History) != 3 {
		t.Fatal("恢复不得覆盖历史")
	}
	// 恢复不存在的 revision
	if _, err := svc.Restore("admin", 999); err == nil {
		t.Fatal("恢复不存在的 revision 应报错")
	}
}

func TestCorruptControlFallsBackToBackup(t *testing.T) {
	dir := t.TempDir()
	keys := NewTestMasterKey()
	svc, err := Open(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update("admin", 0, func(c *Control) error {
		c.System.BotName = "好配置"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
		return nil
	}, "init"); err != nil {
		t.Fatal(err)
	}
	// 损坏主文件
	controlPath := filepath.Join(dir, "control.json")
	if err := os.WriteFile(controlPath, []byte("{broken json"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc2, err := Open(dir, keys)
	if err != nil {
		t.Fatalf("应从 .bak 恢复: %v", err)
	}
	if svc2.Effective().Bot.Name != "好配置" {
		t.Fatalf(".bak 恢复内容错误: %q", svc2.Effective().Bot.Name)
	}
	// .bak 也损坏时显式报错
	if err := os.WriteFile(controlPath+".bak", []byte("also broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, keys); err == nil {
		t.Fatal("主文件与 .bak 均损坏应报错，不得静默回退空配置")
	}
}

func TestSubscribeNotified(t *testing.T) {
	svc, _ := initService(t)
	ch := make(chan struct{}, 1)
	cancel := svc.Subscribe(func() { ch <- struct{}{} })
	defer cancel()
	if _, err := svc.Update("admin", 1, func(c *Control) error {
		c.System.BotName = "通知测试"
		return nil
	}, "notify"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("订阅者未被通知")
	}
}

func TestConcurrentUpdatesSerialized(t *testing.T) {
	svc, _ := initService(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 全部用过期的 revision：只有一个能成功
			_, err := svc.Update("admin", 1, func(c *Control) error {
				c.System.BotName = "并发" + string(rune('0'+i))
				return nil
			}, "并发")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	var conflicts, oks int
	for err := range errs {
		if errors.Is(err, ErrRevisionConflict) {
			conflicts++
		} else if err == nil {
			oks++
		} else {
			t.Fatalf("意外错误: %v", err)
		}
	}
	if oks != 1 {
		t.Fatalf("并发提交应恰好 1 次成功，实际 %d", oks)
	}
	if conflicts != 7 {
		t.Fatalf("并发冲突应恰好 7 次，实际 %d", conflicts)
	}
	if svc.Revision() != 2 {
		t.Fatal("revision 应只前进一次")
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := DecodeControl([]byte(`{"schema_version":2,"revision":0,"unknown_field":true}`))
	if err == nil {
		t.Fatal("未知字段应被拒绝")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("错误信息应说明未知字段: %v", err)
	}
	// schema 版本不匹配（v3 未来版本）
	_, err = DecodeControl([]byte(`{"schema_version":3,"revision":0}`))
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("schema 版本不匹配应返回 SchemaError，实际 %v", err)
	}
	// v1 旧文件同样返回 SchemaError（供自动迁移识别）
	_, err = DecodeControl([]byte(`{"schema_version":1,"revision":0,"welcome":{}}`))
	if !errors.As(err, &se) || se.Version != 1 {
		t.Fatalf("v1 文件应返回 SchemaError{1}，实际 %v", err)
	}
}
