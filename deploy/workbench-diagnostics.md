# workbench 诊断（服务器 SSH 卡住时用）

症状（2026-11-01 13:5x）：`203.0.113.10:22` TCP 能连上（60 ms），但 8 秒收不到 SSH banner；ICMP 正常，
SSH 客户端报 `Connection timed out during banner exchange`。→ 主机还活着，但 sshd 没能力/没机会回包，
常见原因：内存打满疯狂 swap、遗留的 steamcmd/dst-mods 进程占满 CPU、sshd 被 OOM 杀掉重启前卡住。

阿里云控制台 → 实例 → 远程连接 → **Workbench 远程连接**（免密登录，Web 终端），把下面整段贴进去：

```bash
echo '=== 1. 负载 ==='; uptime
echo '=== 2. 内存 ==='; free -m
echo '=== 3. CPU 前 8 ==='; ps aux --sort=-%cpu | head -8
echo '=== 4. 内存前 8 ==='; ps aux --sort=-%mem | head -8
echo '=== 5. 磁盘 ==='; df -h /
echo '=== 6. OOM 记录 ==='; sudo dmesg -T | grep -iE 'oom|killed process' | tail -10
echo '=== 7. sshd ==='; systemctl is-active ssh; sudo systemctl status ssh --no-pager | head -15
echo '=== 8. fail2ban ==='; sudo fail2ban-client status sshd 2>/dev/null | tail -5
echo '=== 9. 游戏服务 ==='; systemctl is-active dst-server; sudo journalctl -u dst-server -n 15 --no-pager
echo '=== 10. 遗留进程 ==='; ps aux | grep -E 'steamcmd|dst-mods|rsync|apt' | grep -v grep
```

## 看到什么就做什么

| 现象 | 处理 |
|---|---|
| 第 10 项里有多个 `steamcmd.sh` / `dst-mods` / `rsync` | 这些是之前中断的循环留下的，`sudo pkill -f steamcmd.sh`、`sudo pkill -f dst-mods`，再 `ps aux` 确认 |
| `free -m` 的 available 很小、swap 用到 3 GB+ | 内存不够（22 模组已占 2.6 GB）。先 `sudo systemctl stop dst-server` 让系统缓过来，再决定升配 4C8G |
| 第 6 项有 `Out of memory: Killed process` | 同上，明确是 OOM；需要升配或减模组 |
| 第 7 项 `sshd` 不是 active/有 restart 记录 | `sudo systemctl restart ssh` |
| 第 8 项 banned IP 里有 `198.51.100.23` | fail2ban 误封了你：`sudo fail2ban-client set sshd unbanip 198.51.100.23` |
| 全部正常但 SSH 还是不通 | 可能是我这边移动网络到 22 端口的问题；用 Workbench 直接操作即可，不影响排查 |

处理完告诉我就行，我这边会重试 SSH；恢复后先跑 M0 spike（PTY 往返 + SSH 隧道到 Agent socket）。
