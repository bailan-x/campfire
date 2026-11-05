// Command campfire-agent runs the cloud-side Agent: a small JSON-RPC service on
// a unix socket that owns all privileged DST operations (mods, worldgen,
// services, logs). The local panel reaches it through an SSH tunnel.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"campfire/internal/agent"
)

func main() {
	var cfg agent.Config
	flag.StringVar(&cfg.Sock, "sock", "/run/campfire/agent.sock", "unix socket path")
	flag.StringVar(&cfg.ClusterDir, "cluster-dir", "/home/dst/.klei/DoNotStarveTogether/MyDediServer", "cluster directory")
	flag.StringVar(&cfg.InstallDir, "install-dir", "/home/dst/dst-server", "DST dedicated server install dir")
	flag.StringVar(&cfg.Home, "home", "/home/dst", "game user home")
	flag.StringVar(&cfg.GameUser, "game-user", "dst", "OS user that owns the game files")
	flag.StringVar(&cfg.ModsList, "mods-list", "", "mods list file (default <home>/mods.list)")
	flag.StringVar(&cfg.BackupScript, "backup-script", "", "backup script (default <home>/dst-backup.sh)")
	flag.StringVar(&cfg.Version, "version", "0.1.0", "agent version")
	flag.Parse()

	a := agent.New(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("campfire-agent %s starting, socket=%s", cfg.Version, cfg.Sock)
	if err := a.Serve(ctx); err != nil {
		log.Fatalf("agent 退出: %v", err)
	}
}
