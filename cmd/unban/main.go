// unban 是一个运维小工具：查询指定群所有处于禁言状态的成员并解除。
// 用法: unban [-group 群号] [-ws ws://127.0.0.1:3001] [-user QQ号] [-dry-run]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"qqbot/internal/onebot"
)

type member struct {
	UserID          int64  `json:"user_id"`
	Nickname        string `json:"nickname"`
	Card            string `json:"card"`
	ShutUpTimestamp int64  `json:"shut_up_timestamp"`
	ShoutTime       int64  `json:"shout_time"`
}

func main() {
	groupID := flag.Int64("group", 0, "目标群号")
	wsURL := flag.String("ws", "ws://127.0.0.1:3001", "NapCat WS 地址")
	token := flag.String("token", "", "AccessToken")
	dryRun := flag.Bool("dry-run", false, "只列出不禁言")
	users := flag.String("user", "", "直接解除指定 QQ（逗号分隔），不查列表")
	flag.Parse()
	if *groupID == 0 {
		fmt.Println("请指定 -group 群号")
		os.Exit(1)
	}

	client := onebot.New(*wsURL, *token, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go client.Run(ctx)

	// 等待连接建立
	for {
		_, err := client.Call("get_group_info", map[string]any{"group_id": *groupID})
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			fmt.Println("无法连接 NapCat:", err)
			os.Exit(1)
		case <-time.After(2 * time.Second):
		}
	}

	now := time.Now().Unix()

	// 指定用户直接解除
	if *users != "" {
		for _, s := range strings.Split(*users, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			if err != nil {
				fmt.Printf("无效 QQ: %q\n", s)
				continue
			}
			if err := client.SetGroupBan(*groupID, id, 0); err != nil {
				fmt.Printf("❌ %d 解除失败: %v\n", id, err)
			} else {
				fmt.Printf("✅ %d 已解除禁言\n", id)
			}
			time.Sleep(500 * time.Millisecond)
		}
		return
	}

	// 否则扫描全群禁言状态
	var list []member
	data, err := client.Call("get_group_member_list", map[string]any{"group_id": *groupID})
	if err != nil {
		fmt.Println("获取成员列表失败:", err)
		os.Exit(1)
	}
	if err := json.Unmarshal(data, &list); err != nil {
		fmt.Println("解析成员列表失败:", err)
		os.Exit(1)
	}

	banned := 0
	for _, m := range list {
		until := m.ShutUpTimestamp
		if until == 0 {
			until = m.ShoutTime
		}
		if until <= now {
			continue
		}
		banned++
		name := m.Card
		if name == "" {
			name = m.Nickname
		}
		left := time.Duration(until-now) * time.Second
		fmt.Printf("发现禁言: %s (%d) 剩余 %s\n", name, m.UserID, left.Round(time.Minute))
		if *dryRun {
			continue
		}
		if err := client.SetGroupBan(*groupID, m.UserID, 0); err != nil {
			fmt.Printf("  解除失败: %v\n", err)
		} else {
			fmt.Printf("  ✅ 已解除 %s (%d)\n", name, m.UserID)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if banned == 0 {
		fmt.Println("该群没有处于禁言状态的成员")
	}
}
