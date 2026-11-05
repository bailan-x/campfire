# M0 spike：验证面板的两条命脉

对应设计文档：C1（无 RCON，只能 stdin + marker）、§3.3 / §6.13（SSH 隧道 + Agent）、§5.8（面板离线）。

三个子命令：

| 命令 | 验证什么 | 跑在哪 |
|---|---|---|
| `spike pty` | SSH + PTY + stdin/stdout 往返，随机 marker 判定命令结束 | 本机连服务器 |
| `spike agent` | 最小 Agent：unix socket + JSON 行协议 | 云服务器 |
| `spike tunnel` | 面板侧用 `ssh direct-streamlocal` 穿透到 unix socket，不占额外端口 | 本机连服务器 |

## 0. 先确认服务器可用

```bash
ssh -i <SSH_KEY_PATH> ops@203.0.113.10 'echo ok'
```
（如果卡在 `banner exchange`，见 `E:\gamedeepseek\deploy\workbench-diagnostics.md`）

## 1. PTY 往返（不需要部署任何东西）

```powershell
E:\gamedeepseek\spike\bin\spike.exe pty `
  -addr 203.0.113.10:22 -user ops -key <SSH_KEY_PATH> `
  -remote "python3 -i -q"
```

**通过标准**：输出 `OK marker round trip: xx ms`，且 `--- remote output ---` 里能看到 marker 那一行。

这一步等价于"往 DST 的 stdin 写命令、读到 marker 就认为执行完了"。真实 DST 场景的差别只是把 `-remote`
换成游戏进程（Agent 用 PTY 托管它），命令换成 `c_announce("...")` 之类。

## 2. 部署最小 Agent

```powershell
E:\gamedeepseek\spike\install-agent.ps1
```

它会做：scp `spike-linux` → 服务器 `/tmp`，`install` 到 `/usr/local/bin/campfire-spike-agent`，
写 systemd unit（`/run/campfire/agent.sock`，以 `dst` 用户运行），`enable --now`，最后一并输出 `status`。

## 3. 隧道调用 Agent

```powershell
E:\gamedeepseek\spike\bin\spike.exe tunnel `
  -addr 203.0.113.10:22 -user ops -key <SSH_KEY_PATH> `
  -sock /run/campfire/agent.sock -op status -rounds 5
```

**通过标准**：5 次全部返回 `"ok":true`，平均耗时打印出来（正常情况下几十毫秒）。

## 4. 结论怎么用

- 两条都通 → 面板按现方案做：Agent 常驻 + 面板只用 SSH 一条通道，不开放额外端口。
- `pty` 通、`tunnel` 不通 → 服务器 sshd 没开 `AllowStreamLocalForwarding`，改走"Agent 监听 127.0.0.1 端口 + `ssh -L` 转发"，文档 §6.13 有备选。
- 两个都不通 → 先修 SSH 通道再谈面板。

## 构建

```powershell
$env:Path="E:\gamedeepseek\tools\go\bin;$env:Path"; $env:GOPROXY='https://goproxy.cn,direct'
cd E:\gamedeepseek\spike
go build -o bin\spike.exe .
$env:GOOS='linux'; go build -o bin\spike-linux .
```
