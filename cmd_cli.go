package main

// CLI 工具（设计文档 19 节）：停机状态下管理配置与凭据。
//
//	qqbot admin reset-password --data-dir ... [--password ...]
//	qqbot secrets rotate --data-dir ... --old-key-file ... --new-key-file ...
//	qqbot config validate --data-dir ... --master-key-file ...
//
// 所有命令默认要求服务停机（避免跨进程同时写文件）。

import (
	"flag"
	"fmt"
	"os"

	"golang.org/x/term"

	"qqbot/internal/admin"
	"qqbot/internal/state"
)

// runCLI 处理子命令；返回 true 表示已处理（进程应退出）。
func runCLI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "admin":
		os.Exit(runAdminCmd(args[1:]))
	case "secrets":
		os.Exit(runSecretsCmd(args[1:]))
	case "config":
		os.Exit(runConfigCmd(args[1:]))
	case "notify":
		os.Exit(runNotifyCmd(args[1:]))
	case "help", "-h", "--help":
		printCLIUsage()
		os.Exit(0)
	}
	return false
}

func printCLIUsage() {
	fmt.Print(`qqbot 管理 CLI（默认要求服务停机）:

  qqbot admin reset-password --data-dir <dir> [--password <pw>]
      重置管理员密码（交互式读取，不传 --password 时从终端输入两次）。

  qqbot secrets rotate --data-dir <dir> --old-key-file <f> --new-key-file <f>
      用新主密钥重加密 control.json 中的全部敏感字段
      （revision 不变；自动备份为 control.json.pre-rotate）。

  qqbot config validate --data-dir <dir> --master-key-file <f>
      校验 control.json 完整性与当前主密钥匹配情况。

  qqbot notify send --server <url> --token <t> --to <qq> "消息"
      通过 QQ 消息向管理员传信（AI agent 进度通报；见 cmd_notify.go / docs/NOTIFY.md）。

环境变量：QQBOT_ADMIN_LISTEN / QQBOT_DATA_DIR / QQBOT_MASTER_KEY_FILE / QQBOT_IMPORT_CONFIG
`)
}

func runAdminCmd(args []string) int {
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	dataDir := fs.String("data-dir", envOr("QQBOT_DATA_DIR", "data"), "数据目录")
	pwArg := fs.String("password", "", "新密码（不传则从终端交互输入，推荐）")
	if len(args) == 0 || args[0] != "reset-password" {
		fmt.Fprintln(os.Stderr, "用法: qqbot admin reset-password --data-dir <dir> [--password <pw>]")
		return 2
	}
	_ = fs.Parse(args[1:])
	pw := *pwArg
	if pw == "" {
		var err error
		pw, err = readPasswordTwice()
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取密码失败:", err)
			return 1
		}
	}
	if err := admin.ResetAdminPassword(*dataDir, pw); err != nil {
		fmt.Fprintln(os.Stderr, "重置密码失败:", err)
		return 1
	}
	fmt.Println("管理员密码已重置（网页会话在进程重启后全部失效）")
	return 0
}

// readPasswordTwice 终端交互读取新密码（不回显，两次一致才返回）。
func readPasswordTwice() (string, error) {
	fmt.Print("新密码（输入不回显）: ")
	pw1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	fmt.Print("再次输入确认: ")
	pw2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	if string(pw1) != string(pw2) {
		return "", fmt.Errorf("两次输入不一致")
	}
	return string(pw1), nil
}

func runSecretsCmd(args []string) int {
	fs := flag.NewFlagSet("secrets", flag.ExitOnError)
	dataDir := fs.String("data-dir", envOr("QQBOT_DATA_DIR", "data"), "数据目录")
	oldKeyFile := fs.String("old-key-file", "", "旧主密钥文件")
	newKeyFile := fs.String("new-key-file", "", "新主密钥文件")
	if len(args) == 0 || args[0] != "rotate" {
		fmt.Fprintln(os.Stderr, "用法: qqbot secrets rotate --data-dir <dir> --old-key-file <f> --new-key-file <f>")
		return 2
	}
	_ = fs.Parse(args[1:])
	if *oldKeyFile == "" || *newKeyFile == "" {
		fmt.Fprintln(os.Stderr, "需要 --old-key-file 与 --new-key-file")
		return 2
	}
	oldKey, err := state.LoadMasterKey(*oldKeyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载旧主密钥失败:", err)
		return 1
	}
	newKey, err := state.LoadMasterKey(*newKeyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载新主密钥失败:", err)
		return 1
	}
	rotated, err := state.RotateSecrets(*dataDir, oldKey, newKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "密钥轮换失败:", err)
		return 1
	}
	if len(rotated) == 0 {
		fmt.Println("无需轮换（未配置敏感字段或已使用新密钥）")
		return 0
	}
	fmt.Printf("密钥轮换完成：%v（备份：control.json.pre-rotate）\n", rotated)
	fmt.Println("请更新 QQBOT_MASTER_KEY_FILE 指向新主密钥，并安全销毁旧密钥文件")
	return 0
}

func runConfigCmd(args []string) int {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	dataDir := fs.String("data-dir", envOr("QQBOT_DATA_DIR", "data"), "数据目录")
	keyFile := fs.String("master-key-file", os.Getenv("QQBOT_MASTER_KEY_FILE"), "主密钥文件")
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(os.Stderr, "用法: qqbot config validate --data-dir <dir> --master-key-file <f>")
		return 2
	}
	_ = fs.Parse(args[1:])
	if *keyFile == "" {
		fmt.Fprintln(os.Stderr, "需要 --master-key-file")
		return 2
	}
	keys, err := state.LoadMasterKey(*keyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载主密钥失败:", err)
		return 1
	}
	svc, err := state.Open(*dataDir, keys)
	if err != nil {
		fmt.Fprintln(os.Stderr, "校验失败:", err)
		return 1
	}
	if !svc.Initialized() {
		fmt.Println("control.json 未初始化（等待网页 setup）——校验通过")
		return 0
	}
	cur := svc.Current()
	eff := svc.Effective()
	fmt.Printf("校验通过：revision=%d groups=%d owner=%d onebot=%s napcat=%s\n",
		cur.Revision, len(cur.Groups), cur.System.Owner,
		cur.System.OneBot.WSURL, orDash(cur.System.NapCat.WebUIURL))
	_ = eff
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
