// Package panel is the local Windows UI + control plane. It never talks to the
// game host directly: every call travels through an SSH connection to the
// Agent's unix socket (design doc §3.3).
package panel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// NodeConfig describes how to reach one cloud host.
type NodeConfig struct {
	Host       string
	Port       int
	User       string
	KeyPath    string
	KnownHosts string
	SockPath   string
}

// RPC tunnels JSON-line requests to the Agent over SSH.
type RPC struct {
	cfg NodeConfig

	mu     sync.Mutex
	client *ssh.Client
	conn   net.Conn
	r      *bufio.Reader
	seq    int64
}

func NewRPC(cfg NodeConfig) *RPC {
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.SockPath == "" {
		cfg.SockPath = "/run/campfire/agent.sock"
	}
	return &RPC{cfg: cfg}
}

// Close drops the tunnel (it is re-established lazily).
func (c *RPC) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropLocked()
}

func (c *RPC) dropLocked() error {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	if c.client != nil {
		_ = c.client.Close()
		c.client = nil
	}
	return nil
}

func (c *RPC) connectLocked() error {
	if c.conn != nil {
		return nil
	}
	key, err := os.ReadFile(c.cfg.KeyPath)
	if err != nil {
		return fmt.Errorf("读取 SSH 私钥 %s 失败: %w", c.cfg.KeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("解析 SSH 私钥失败: %w", err)
	}
	hostKeyCB := ssh.InsecureIgnoreHostKey()
	if c.cfg.KnownHosts != "" {
		if _, statErr := os.Stat(c.cfg.KnownHosts); statErr == nil {
			if cb, cbErr := knownhosts.New(c.cfg.KnownHosts); cbErr == nil {
				hostKeyCB = cb
			}
		}
	}
	cc := &ssh.ClientConfig{
		User:            c.cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCB,
		Timeout:         15 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)
	client, err := ssh.Dial("tcp", addr, cc)
	if err != nil {
		return fmt.Errorf("SSH 连接 %s 失败: %w", addr, err)
	}
	conn, err := client.Dial("unix", c.cfg.SockPath)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("SSH 已连通，但打不开 Agent socket %s（Agent 未运行？）: %w", c.cfg.SockPath, err)
	}
	c.client = client
	c.conn = conn
	c.r = bufio.NewReaderSize(conn, 1<<20)
	return nil
}

// Call performs one RPC and decodes the result into out (may be nil).
func (c *RPC) Call(op string, params interface{}, out interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.connectLocked(); err != nil {
		return err
	}
	req := map[string]interface{}{"id": time.Now().UnixNano(), "op": op}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req["params"] = json.RawMessage(raw)
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_ = c.conn.SetDeadline(time.Now().Add(100 * time.Second))
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		_ = c.dropLocked()
		return fmt.Errorf("写入隧道失败: %w", err)
	}
	respLine, err := c.r.ReadBytes('\n')
	if err != nil {
		_ = c.dropLocked()
		return fmt.Errorf("读取隧道响应失败: %w", err)
	}
	var resp struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	dec := json.NewDecoder(bytes.NewReader(respLine))
	if err := dec.Decode(&resp); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("解析结果失败: %w", err)
		}
	}
	return nil
}
