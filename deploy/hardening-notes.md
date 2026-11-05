# 云服务器安全加固记录（2026-11-01）

服务器：203.0.113.10（i-bp1bkgdxvphnvgddu9ch，华东1 杭州，Ubuntu 22.04 LTS）

## 变更前

- 唯一可登录账号：`root`，`PermitRootLogin yes`、`PasswordAuthentication no`（仅密钥）
- 无 fail2ban、无自动安全更新
- 安全组：TCP 22 → 0.0.0.0/0（阿里云高危红标）；ICMP 全部放行

## 变更后

| 项 | 状态 |
|---|---|
| 管理账号 | 新增 `ops`（uid 1001，组 sudo，sudo NOPASSWD） |
| SSH 密钥 | `/home/ops/.ssh/authorized_keys` = 本机 `<SSH_KEY_PATH>` 对应公钥 |
| PermitRootLogin | **no**（root 已无法 SSH 登录，实测 Permission denied） |
| PasswordAuthentication | no |
| PubkeyAuthentication | yes / KbdInteractiveAuthentication no / MaxAuthTries 4 |
| 配置文件 | `/etc/ssh/sshd_config.d/00-campfire.conf` |
| fail2ban | active，jail `sshd`，maxretry 5 / findtime 10m / bantime 1h，backend=systemd |
| 自动安全更新 | unattended-upgrades 已装并启用，`Automatic-Reboot "false"`（避免重启杀游戏进程） |
| 日常连接 | `ssh -i <SSH_KEY_PATH> ops@203.0.113.10` |

## 未变 / 仍待办

- 安全组仍需补三条 **UDP** 入方向：`10998/11018`、`27015/27020`、`8766/8770` → `0.0.0.0/0`，优先级 1
- 阿里云控制台的“SSH 22 高危”红标仍会显示（按来源判定），实际风险已显著降低
- root 的补丁/维护操作统一通过 `ops` + `sudo` 执行
