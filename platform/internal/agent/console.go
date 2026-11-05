package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"campfire/internal/proto"
)

// DST has no RCON. The dedicated server only accepts console commands on its
// stdin, so every shard is started through a tiny wrapper that pipes a command
// file into the process. The agent appends commands to that file, then reads the
// shard's own log to find the begin/end markers it bracketed the command with.
const (
	consoleDir      = "/run/campfire/console"
	shardRunnerPath = "/usr/local/bin/campfire-shard-run"
)

const shardRunner = `#!/bin/sh
# Campfire shard runner — feeds this shard's console command file to the game.
# DST has no RCON: a running shard can only be commanded through stdin.
# argv: <cluster> <shard> <install-dir>
set -u
cluster="$1"
shard="$2"
install="$3"
dir="` + consoleDir + `"
cmd="$dir/$cluster.$shard.cmd"
mkdir -p "$dir" 2>/dev/null || true
[ -e "$cmd" ] || : > "$cmd" 2>/dev/null || true
exec tail -n 0 -F "$cmd" | exec "$install/bin64/dontstarve_dedicated_server_nullrenderer_x64" -console -cluster "$cluster" -shard "$shard"
`

// installShardRunner writes the stdin wrapper (idempotent).
func (a *Agent) installShardRunner() error {
	return writeFileAtomic(shardRunnerPath, []byte(shardRunner), 0o755)
}

// consoleDirReady creates the command-file directory. It is owned by the game
// user (the wrapper runs as that user and creates its own file); the agent runs
// as root so it can always append.
func (a *Agent) consoleDirReady(ctx context.Context) error {
	if err := os.MkdirAll(consoleDir, 0o775); err != nil {
		return err
	}
	_, _ = a.run.run(ctx, 10*time.Second, "chown", a.cfg.GameUser+":"+a.cfg.Group, consoleDir)
	return os.Chmod(consoleDir, 0o775)
}

func consoleCmdPath(c clusterDef, shard string) string {
	return filepath.Join(consoleDir, c.Name+"."+shard+".cmd")
}

// bootstrapConsole upgrades this install to the stdin-console layout: the
// wrapper is installed, the console dir exists, and shard units are rewritten to
// run through the wrapper. Shards that are already running keep their old
// ExecStart until they are restarted once — consoleExec reports that clearly.
func (a *Agent) bootstrapConsole(ctx context.Context) {
	if err := a.installShardRunner(); err != nil {
		a.run.logf("install shard runner: %v", err)
	}
	if err := a.consoleDirReady(ctx); err != nil {
		a.run.logf("console dir: %v", err)
	}
	rf, err := a.loadRegistry()
	if err != nil {
		a.run.logf("console registry: %v", err)
		return
	}
	changed := false
	for _, c := range rf.Clusters {
		stale := false
		for _, sh := range c.shards(a.cfg) {
			data, err := os.ReadFile(filepath.Join("/etc/systemd/system", sh.Service+".service"))
			if err != nil {
				continue
			}
			if !strings.Contains(string(data), "campfire-shard-run") {
				stale = true
				break
			}
		}
		if !stale {
			continue
		}
		if err := a.writeUnits(c); err != nil {
			a.run.logf("rewrite units for %s: %v", c.Name, err)
			continue
		}
		a.run.logf("units for %s rewritten to the console wrapper (takes effect on next restart)", c.Name)
		changed = true
	}
	if changed {
		if _, err := a.run.run(ctx, 30*time.Second, "systemctl", "daemon-reload"); err != nil {
			a.run.logf("daemon-reload: %v", err)
		}
	}
}

// consoleExec runs one console command on a shard and returns what the shard
// printed between the two markers.
func (a *Agent) consoleExec(ctx context.Context, p proto.ConsoleExecParams) (*proto.ConsoleExecResult, error) {
	c, err := a.resolveCluster(p.Cluster)
	if err != nil {
		return nil, err
	}
	shardName := strings.TrimSpace(p.Shard)
	if shardName == "" {
		shardName = "Master"
	}
	svc := ""
	for _, sh := range c.shards(a.cfg) {
		if strings.EqualFold(sh.Name, shardName) {
			svc = sh.Service
			shardName = sh.Name
			break
		}
	}
	if svc == "" {
		return nil, fmt.Errorf("世界「%s」没有 %s 分片", c.Name, shardName)
	}
	command := strings.TrimSpace(p.Command)
	if command == "" {
		return nil, fmt.Errorf("控制台命令不能为空")
	}
	if strings.ContainsAny(command, "\r\n") {
		return nil, fmt.Errorf("一次只能执行一条控制台命令（不能包含换行）")
	}
	if err := a.consoleDirReady(ctx); err != nil {
		return nil, fmt.Errorf("准备控制台目录失败：%w", err)
	}
	// stdin is only wired once the unit runs through the wrapper.
	out, err := a.run.run(ctx, 10*time.Second, "systemctl", "show", "-p", "ExecStart", "--value", svc)
	if err == nil && !strings.Contains(out, "campfire-shard-run") {
		return nil, fmt.Errorf("分片 %s 仍是旧启动方式：请先在面板里重启一次该分片，控制台才会生效", shardName)
	}
	active, _ := a.run.serviceState(ctx, svc)
	if !active {
		return nil, fmt.Errorf("分片 %s 没在运行，先启动再执行控制台命令", shardName)
	}

	logPath := filepath.Join(c.Dir, shardName, "server_log.txt")
	var offset int64
	if fi, statErr := os.Stat(logPath); statErr == nil {
		offset = fi.Size()
	}

	token := fmt.Sprintf("%x", time.Now().UnixNano())
	startMark := "CFM-" + token
	doneMark := "CFD-" + token
	f, err := os.OpenFile(consoleCmdPath(c, shardName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		return nil, fmt.Errorf("打开控制台输入失败：%w", err)
	}
	_, werr := fmt.Fprintf(f, "print(\"%s\")\n%s\nprint(\"%s\")\n", startMark, command, doneMark)
	cerr := f.Close()
	if werr != nil {
		return nil, fmt.Errorf("写入控制台命令失败：%w", werr)
	}
	if cerr != nil {
		return nil, fmt.Errorf("关闭控制台输入失败：%w", cerr)
	}

	lines, err := a.waitConsoleMarkers(ctx, logPath, offset, startMark, doneMark, 25*time.Second)
	if err != nil {
		return nil, err
	}
	return &proto.ConsoleExecResult{Shard: shardName, Command: command, Lines: lines}, nil
}

// waitConsoleMarkers tails the shard log from offset until the closing marker
// shows up, returning the lines printed in between (the DST log prefixes each
// line with a "[HH:MM:SS]:" stamp, which is stripped here).
func (a *Agent) waitConsoleMarkers(ctx context.Context, logPath string, offset int64, startMark, doneMark string, timeout time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr string
	for {
		if lines, ok, err := readBetweenMarkers(logPath, offset, startMark, doneMark); err != nil {
			lastErr = err.Error()
		} else if ok {
			return lines, nil
		}
		if time.Now().After(deadline) {
			if lastErr != "" {
				return nil, fmt.Errorf("命令已发送，但读取分片日志失败：%s", lastErr)
			}
			return nil, fmt.Errorf("命令已发送，但 25 秒内没在分片日志里看到完成标记：游戏可能没把这条命令的控制台输出写进 server_log.txt")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func readBetweenMarkers(path string, offset int64, startMark, doneMark string) ([]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if fi.Size() <= offset {
		return nil, false, nil
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return nil, false, err
	}
	data := make([]byte, fi.Size()-offset)
	if _, err := f.Read(data); err != nil {
		return nil, false, err
	}
	started := false
	var out []string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(stripLogStamp(raw), " \t")
		if strings.Contains(line, doneMark) {
			return out, true, nil
		}
		if started && line != "" {
			// 标记行和命令回显（RemoteCommandInput: …）不是命令的输出，丢掉。
			if strings.Contains(line, startMark) || strings.HasPrefix(line, "RemoteCommandInput:") {
				continue
			}
			out = append(out, line)
		}
		if strings.Contains(line, startMark) {
			started = true
		}
	}
	return nil, false, nil
}

// stripLogStamp removes DST's leading "[00:12:34]: " timestamp from a log line.
func stripLogStamp(line string) string {
	if !strings.HasPrefix(line, "[") {
		return line
	}
	if i := strings.Index(line, "]: "); i > 0 && i < 16 {
		return line[i+3:]
	}
	return line
}
