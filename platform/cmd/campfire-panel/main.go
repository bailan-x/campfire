// Command campfire-panel runs the local management UI on the player's Windows
// machine. It only listens on loopback and reaches the cloud Agent through SSH.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"campfire/internal/panel"
)

// panelConfig is stored in %APPDATA%\campfire\panel.json so the panel can be
// started with a double click instead of a long command line.
type panelConfig struct {
	Host        string   `json:"host"`
	SSHPort     int      `json:"ssh_port"`
	SSHUser     string   `json:"ssh_user"`
	SSHKey      string   `json:"ssh_key"`
	KnownHosts  string   `json:"known_hosts"`
	Addr        string   `json:"addr"`
	Sock        string   `json:"sock"`
	PublicHosts []string `json:"public_hosts"`
	AuthUser    string   `json:"auth_user"`
	AuthPass    string   `json:"auth_pass"`
	ReadOnly    bool     `json:"read_only"`
}

func defaultConfigPath() string {
	dir := os.Getenv("APPDATA")
	if dir == "" {
		dir = os.Getenv("HOME")
	}
	return filepath.Join(dir, "campfire", "panel.json")
}

func loadConfig(path string) (panelConfig, bool) {
	cfg := panelConfig{
		SSHPort: 22,
		SSHUser: "ops",
		Addr:    "127.0.0.1:8717",
		Sock:    "/run/campfire/agent.sock",
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, false
	}
	_ = json.Unmarshal(data, &cfg)
	if cfg.SSHPort == 0 {
		cfg.SSHPort = 22
	}
	if cfg.SSHUser == "" {
		cfg.SSHUser = "ops"
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8717"
	}
	if cfg.Sock == "" {
		cfg.Sock = "/run/campfire/agent.sock"
	}
	return cfg, true
}

func saveConfig(path string, cfg panelConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "配置文件路径（默认 %APPDATA%\\campfire\\panel.json）")
	addr := flag.String("addr", "", "面板监听地址（默认 127.0.0.1:8717）")
	host := flag.String("host", "", "云服务器公网 IP")
	port := flag.Int("ssh-port", 0, "SSH 端口")
	user := flag.String("ssh-user", "", "SSH 用户名")
	keyPath := flag.String("ssh-key", "", "SSH 私钥路径")
	knownHosts := flag.String("known-hosts", "", "known_hosts 路径")
	sockPath := flag.String("sock", "", "云服务器上的 Agent socket 路径")
	publicHosts := flag.String("public-host", "", "允许通过隧道域名访问（逗号分隔，支持 *.example.com 通配）；默认只允许本机")
	authUser := flag.String("auth-user", "", "公网访问的用户名（默认 campfire）")
	authPass := flag.String("auth-pass", "", "公网访问的口令（留空且配置了 public_hosts 时自动生成）")
	readOnly := flag.Bool("read-only", false, "公网访问只读（只允许查看，写操作一律拒绝）")
	flag.Parse()

	cfg, existed := loadConfig(*cfgPath)
	// 命令行参数覆盖配置文件
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *host != "" {
		cfg.Host = *host
	}
	if *port != 0 {
		cfg.SSHPort = *port
	}
	if *user != "" {
		cfg.SSHUser = *user
	}
	if *keyPath != "" {
		cfg.SSHKey = *keyPath
	}
	if *knownHosts != "" {
		cfg.KnownHosts = *knownHosts
	}
	if *sockPath != "" {
		cfg.Sock = *sockPath
	}
	if *publicHosts != "" {
		cfg.PublicHosts = splitList(*publicHosts)
	}
	if *authUser != "" {
		cfg.AuthUser = *authUser
	}
	if *authPass != "" {
		cfg.AuthPass = *authPass
	}
	if *readOnly {
		cfg.ReadOnly = true
	}

	// 公网访问必须有口令：没配置就自动生成一个并写回配置文件。
	if len(cfg.PublicHosts) > 0 {
		if cfg.AuthUser == "" {
			cfg.AuthUser = "campfire"
		}
		if cfg.AuthPass == "" {
			cfg.AuthPass = randomPassword()
			if err := saveConfig(*cfgPath, cfg); err != nil {
				log.Printf("口令已生成但写回配置失败: %v", err)
			}
		}
	}

	if cfg.Host == "" || cfg.SSHKey == "" {
		log.Fatalf("缺少配置：需要云服务器公网 IP 与 SSH 私钥路径。\n"+
			"  首次使用：campfire-panel -host <公网IP> -ssh-key <私钥路径> -known-hosts <known_hosts>\n"+
			"  配置文件：%s", *cfgPath)
	}
	if !existed {
		if err := saveConfig(*cfgPath, cfg); err == nil {
			log.Printf("已保存配置到 %s", *cfgPath)
		}
	}

	rpc := panel.NewRPC(panel.NodeConfig{
		Host:       cfg.Host,
		Port:       cfg.SSHPort,
		User:       cfg.SSHUser,
		KeyPath:    cfg.SSHKey,
		KnownHosts: cfg.KnownHosts,
		SockPath:   cfg.Sock,
	})
	defer rpc.Close()
	srv := panel.NewServer(rpc, cfg.Addr, cfg.PublicHosts, cfg.AuthUser, cfg.AuthPass, cfg.ReadOnly)
	fmt.Printf("Campfire 面板已启动：http://%s\n（连接 %s@%s:%d，Agent socket %s）\n", cfg.Addr, cfg.SSHUser, cfg.Host, cfg.SSHPort, cfg.Sock)
	if len(cfg.PublicHosts) > 0 {
		fmt.Printf("公网访问：允许 Host %s\n", strings.Join(cfg.PublicHosts, ", "))
		fmt.Printf("  登录用户名：%s\n", cfg.AuthUser)
		fmt.Printf("  登录口令：%s\n", cfg.AuthPass)
		fmt.Printf("  只读模式：%v（公网只能查看，写操作会被拒绝；本机 127.0.0.1 仍为完整权限）\n", cfg.ReadOnly)
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("面板退出: %v", err)
	}
}

// randomPassword returns a random "cf-" prefixed password for public access.
func randomPassword() string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("cf-%d", time.Now().UnixNano())
	}
	return "cf-" + hex.EncodeToString(buf)
}

// splitList splits a comma separated flag value, trimming spaces and empties.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
