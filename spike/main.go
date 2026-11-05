// campfire spike —— 验证面板的两条命脉
//
//  1) pty      : SSH + PTY + stdin/stdout 往返，用随机 marker 判定"命令已执行完"，
//                对应设计文档 C1（DST 没有 RCON，只能靠 stdin + marker 判断结束）。
//  2) agent    : 最小 Agent，监听 unix socket，JSON 行协议（跑在云服务器上）。
//  3) tunnel   : 面板侧 —— 用 SSH 的 direct-streamlocal 穿透到远程 unix socket，
//                对应 §3.3 / §6.13 的通道设计（不额外开放端口）。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: spike <pty|agent|tunnel> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "pty":
		cmdPTY(os.Args[2:])
	case "agent":
		cmdAgent(os.Args[2:])
	case "tunnel":
		cmdTunnel(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

type sshOpts struct {
	addr        string
	user        string
	keyPath     string
	fingerprint string
	timeout     time.Duration
}

func (o sshOpts) dial() (*ssh.Client, error) {
	raw, err := os.ReadFile(o.keyPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	var cb ssh.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	var seenKey string
	if o.fingerprint != "" {
		want := o.fingerprint
		cb = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got := ssh.FingerprintSHA256(key)
			seenKey = got
			if !strings.EqualFold(got, want) {
				return fmt.Errorf("host key mismatch: got %s, want %s", got, want)
			}
			return nil
		}
	}
	cfg := &ssh.ClientConfig{
		User:            o.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: cb,
		Timeout:         o.timeout,
	}
	start := time.Now()
	client, err := ssh.Dial("tcp", o.addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	fmt.Printf("ssh connected in %s (host key %s)\n", time.Since(start).Round(time.Millisecond), seenKey)
	return client, nil
}

func addCommonFlags(fs *flag.FlagSet) *sshOpts {
	o := &sshOpts{}
	fs.StringVar(&o.addr, "addr", "", "host:port，例如 203.0.113.10:22")
	fs.StringVar(&o.user, "user", "ops", "登录用户")
	fs.StringVar(&o.keyPath, "key", "", "私钥文件路径")
	fs.StringVar(&o.fingerprint, "fingerprint", "", "期望的主机密钥指纹，如 SHA256:xxxx（留空则不校验）")
	fs.DurationVar(&o.timeout, "timeout", 20*time.Second, "连接超时")
	return o
}

// ---------- 1) PTY 往返 ----------

func cmdPTY(args []string) {
	fs := flag.NewFlagSet("pty", flag.ExitOnError)
	o := addCommonFlags(fs)
	remote := fs.String("remote", "python3 -i -q", "远程交互式程序（DST 场景下换成游戏服务器进程）")
	wait := fs.Duration("wait", 25*time.Second, "等待 marker 的总超时")
	fs.Parse(args)
	if o.addr == "" || o.keyPath == "" {
		fs.Usage()
		os.Exit(2)
	}

	client, err := o.dial()
	if err != nil {
		fail(err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		fail(fmt.Errorf("new session: %w", err))
	}
	defer sess.Close()

	// ECHO=0：关掉终端回显，免得 marker 被回显两次；真实场景按需打开。
	modes := ssh.TerminalModes{ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", 40, 120, modes); err != nil {
		fail(fmt.Errorf("request pty: %w", err))
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		fail(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		fail(err)
	}

	mark := fmt.Sprintf("MARK-%d", time.Now().UnixNano())
	start := time.Now()
	if err := sess.Start(*remote); err != nil {
		fail(fmt.Errorf("start %q: %w", *remote, err))
	}
	fmt.Printf("remote program started: %s\n", *remote)

	// 真实场景是"写命令 → 读输出直到 marker"；这里先用一条打印语句代替。
	go func() {
		time.Sleep(400 * time.Millisecond)
		fmt.Fprintf(stdin, "print('%s')\n", mark)
	}()

	outCh := make(chan string, 1)
	go func() {
		var b strings.Builder
		r := bufio.NewReader(stdout)
		for {
			line, err := r.ReadString('\n')
			b.WriteString(line)
			if strings.Contains(b.String(), mark) {
				outCh <- b.String()
				return
			}
			if err != nil {
				outCh <- b.String() + "\nEOF:" + err.Error()
				return
			}
		}
	}()

	select {
	case out := <-outCh:
		fmt.Printf("OK  marker round trip: %s\n", time.Since(start).Round(time.Millisecond))
		fmt.Printf("--- remote output ---\n%s----------------------\n", out)
	case <-time.After(*wait):
		fmt.Println("TIMEOUT: 没有等到 marker（说明这条通道不能用来判断命令结束）")
		sess.Signal(ssh.SIGKILL)
		os.Exit(1)
	}
}

// ---------- 2) 最小 Agent ----------

type agentReq struct {
	ID int    `json:"id"`
	Op string `json:"op"`
}

type agentResp struct {
	ID      int    `json:"id"`
	OK      bool   `json:"ok"`
	Op      string `json:"op"`
	Host    string `json:"host,omitempty"`
	PID     int    `json:"pid,omitempty"`
	UptimeS int64  `json:"uptime_s,omitempty"`
	TS      string `json:"ts"`
	Err     string `json:"err,omitempty"`
}

func cmdAgent(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	sock := fs.String("sock", "/run/campfire/agent.sock", "unix socket 路径")
	fs.Parse(args)

	_ = os.Remove(*sock)
	ln, err := net.Listen("unix", *sock)
	if err != nil {
		fail(fmt.Errorf("listen %s: %w", *sock, err))
	}
	_ = os.Chmod(*sock, 0o660)
	started := time.Now()
	host, _ := os.Hostname()
	fmt.Printf("agent listening on %s (host=%s pid=%d)\n", *sock, host, os.Getpid())

	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept: %v\n", err)
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				var req agentReq
				if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
					writeJSON(c, agentResp{OK: false, Err: "bad json: " + err.Error(), TS: now()})
					continue
				}
				resp := agentResp{ID: req.ID, OK: true, Op: req.Op, TS: now()}
				switch req.Op {
				case "ping":
				case "status":
					resp.Host, resp.PID = host, os.Getpid()
					resp.UptimeS = int64(time.Since(started).Seconds())
				default:
					resp.OK, resp.Err = false, "unknown op"
				}
				writeJSON(c, resp)
			}
		}(conn)
	}
}

// ---------- 3) 面板侧隧道 ----------

func cmdTunnel(args []string) {
	fs := flag.NewFlagSet("tunnel", flag.ExitOnError)
	o := addCommonFlags(fs)
	sock := fs.String("sock", "/run/campfire/agent.sock", "远程 unix socket 路径")
	op := fs.String("op", "status", "要调用的 Agent 操作")
	rounds := fs.Int("rounds", 3, "往返次数（测稳定性与耗时）")
	fs.Parse(args)
	if o.addr == "" || o.keyPath == "" {
		fs.Usage()
		os.Exit(2)
	}

	client, err := o.dial()
	if err != nil {
		fail(err)
	}
	defer client.Close()

	var total time.Duration
	for i := 0; i < *rounds; i++ {
		start := time.Now()
		conn, err := client.Dial("unix", *sock) // 走 direct-streamlocal，不占额外端口
		if err != nil {
			fail(fmt.Errorf("dial unix %s: %w", *sock, err))
		}
		if _, err := fmt.Fprintf(conn, "{\"id\":%d,\"op\":%q}\n", i+1, *op); err != nil {
			fail(err)
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if err != nil {
			fail(fmt.Errorf("read response: %w", err))
		}
		d := time.Since(start)
		total += d
		fmt.Printf("[%d] %s  %s", i+1, d.Round(time.Millisecond), line)
	}
	fmt.Printf("OK  avg round trip over ssh+unix socket: %s\n", (total / time.Duration(*rounds)).Round(time.Millisecond))
}

// ---------- helpers ----------

func writeJSON(w net.Conn, v any) {
	b, _ := json.Marshal(v)
	w.Write(append(b, '\n'))
}

func now() string { return time.Now().Format(time.RFC3339) }

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}
