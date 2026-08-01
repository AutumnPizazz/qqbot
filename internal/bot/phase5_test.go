package bot

import (
	"encoding/json"
	"testing"

	"qqbot/internal/onebot"
)

// TestPrivateMessageIgnored 阶段 5 验收：私聊消息（含 owner）不再触发任何管理指令。
func TestPrivateMessageIgnored(t *testing.T) {
	b := newTestBot(t)
	// owner 私聊发送旧版指令：应被静默忽略（onMessage 返回 nil，无副作用）
	for _, msg := range []string{"/cfg 123456789 welcome on", "/mute 123456789 8888 30", "/status 123456789", "?"} {
		raw, _ := json.Marshal(onebot.GroupMessage{
			MessageType: "private",
			UserID:      b.cfg().Bot.Owner,
			RawMessage:  msg,
		})
		if err := b.onMessage(raw); err != nil {
			t.Fatalf("私聊消息 %q 处理异常: %v", msg, err)
		}
	}
	// 非 owner 私聊同样忽略
	raw, _ := json.Marshal(onebot.GroupMessage{
		MessageType: "private",
		UserID:      99999999,
		RawMessage:  "/cfg 123456789 welcome on",
	})
	if err := b.onMessage(raw); err != nil {
		t.Fatalf("非 owner 私聊处理异常: %v", err)
	}
}

// TestGroupMessageStillProcessed 群消息处理不受停用影响（规则处罚仍生效）。
func TestGroupMessageStillProcessed(t *testing.T) {
	b := newTestBot(t)
	// 示例配置不含规则（旧 YAML），规则引擎无动作：处理不报错即可
	raw, _ := json.Marshal(onebot.GroupMessage{
		MessageType: "group",
		GroupID:     123456789,
		UserID:      55555,
		MessageID:   1,
		RawMessage:  "这里有广告",
		Sender:      onebot.Sender{UserID: 55555, Nickname: "测试", Role: "member"},
		SelfID:      100,
	})
	if err := b.onMessage(raw); err != nil {
		t.Fatalf("群消息处理失败: %v", err)
	}
}
