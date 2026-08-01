// Package onebot 实现 OneBot v11 正向 WebSocket 协议的轻量客户端。
package onebot

import "encoding/json"

// Envelope 是所有事件的公共外壳，用于判断事件类型后分发给具体处理器。
type Envelope struct {
	PostType string `json:"post_type"` // message / notice / request / meta_event
	SelfID   int64  `json:"self_id"`

	// meta_event
	MetaEventType string `json:"meta_event_type,omitempty"`
	// message
	MessageType string `json:"message_type,omitempty"`
	// notice
	NoticeType string `json:"notice_type,omitempty"`
	// request
	RequestType string `json:"request_type,omitempty"`
}

// CQMsg 是一段 CQ 码消息段（如 text / at / image）。
type CQMsg struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Sender 是消息发送者信息。
type Sender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Role     string `json:"role"` // owner / admin / member
	JoinTime int64  `json:"join_time,omitempty"` // 入群时间戳（get_group_member_info 响应）
}

// GroupMessage 是群聊消息事件。
type GroupMessage struct {
	Time        int64  `json:"time"`
	SelfID      int64  `json:"self_id"`
	MessageID   int32  `json:"message_id"`
	MessageType string `json:"message_type"`
	GroupID     int64  `json:"group_id"`
	UserID      int64  `json:"user_id"`
	RawMessage  string `json:"raw_message"`
	Message     []CQMsg
	Sender      Sender `json:"sender"`
	GroupSender Sender `json:"group_sender"` // NapCat 兼容字段
}

// GroupIncrease 是群成员增加通知（新人入群）。
type GroupIncrease struct {
	Time       int64 `json:"time"`
	SelfID     int64 `json:"self_id"`
	GroupID    int64 `json:"group_id"`
	UserID     int64 `json:"user_id"` // 新人 QQ
	OperatorID int64 `json:"operator_id"`
}

// GroupRequest 是加群请求 / 邀请机器人入群请求。
type GroupRequest struct {
	Time        int64  `json:"time"`
	SelfID      int64  `json:"self_id"`
	GroupID     int64  `json:"group_id"`
	UserID      int64  `json:"user_id"`
	Comment     string `json:"comment"`
	Flag        string `json:"flag"`
	SubType     string `json:"sub_type"` // add=加群申请 invite=机器人被邀请
	RequestType string `json:"request_type"`
}

// ActionResponse 是一次 API 调用的响应。
type ActionResponse struct {
	Status  string          `json:"status"` // ok / failed
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    json.RawMessage `json:"echo"`
}
