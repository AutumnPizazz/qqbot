// wsmonitor 临时调试工具：监听 OneBot 事件 60 秒并打印。
// 用法: wsmonitor [-ws url] [-token xxx]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"qqbot/internal/onebot"
)

func main() {
	wsURL := flag.String("ws", "ws://127.0.0.1:3001", "NapCat WS 地址")
	token := flag.String("token", "", "AccessToken")
	flag.Parse()

	client := onebot.New(*wsURL, *token, 5*time.Second)
	client.On("message", func(raw json.RawMessage) error {
		fmt.Printf("[%s] message: %s\n", time.Now().Format("15:04:05"), string(raw)[:min(200, len(string(raw)))])
		return nil
	})
	client.On("notice", func(raw json.RawMessage) error {
		fmt.Printf("[%s] notice: %s\n", time.Now().Format("15:04:05"), string(raw)[:min(200, len(string(raw)))])
		return nil
	})
	client.On("request", func(raw json.RawMessage) error {
		fmt.Printf("[%s] request: %s\n", time.Now().Format("15:04:05"), string(raw)[:min(200, len(string(raw)))])
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go client.Run(ctx)
	fmt.Println("监听中，请发一条私聊给机器人（如 /help）...")
	<-ctx.Done()
	fmt.Println("监听结束")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
