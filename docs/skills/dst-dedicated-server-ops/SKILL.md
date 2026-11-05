---
name: dst-dedicated-server-ops
description: Use when deploying, operating or troubleshooting a Don't Starve Together (DST) dedicated server on Linux/cloud — steamcmd install, systemd units, Master/Caves shards, workshop mods (ugc_mods), worldgen rules, backups, Klei token — or when operating the "Campfire" management panel/agent for such a server. Covers the exact commands and the non-obvious traps that break a real deployment.
---

# DST 专用服务器：部署与运维跑通流程

> 适用：在 Linux（Ubuntu 22.04 已验证）或任何云主机上把 DST 专用服务器跑起来，并接上 Windows 面板（Campfire）。
> 本 skill 是"已验证的流程 + 硬约束"，不是理论。完整背景、踩坑细节、替换点分别见项目里的
> `docs\踩坑与解决办法.md`、`docs\通用化与替换点.md`、`docs\交接说明.md`、`docs\DST服务器管理平台-设计方案.md`。
> 安装方式：把本文件所在的 `dst-dedicated-server-ops\` 目录整体放进 skills 目录（例如 `%APPDATA%\dsh\skills\` 或 `DSH_HOME/skills/`）即可被识别。

## 0. 决定成败的 6 条硬约束（先看这个，违反必炸）

1. **工作目录必须是 `<install>/bin64`**，否则 `Error, failed to change working directory to ../data` + `Error loading main.lua`。
2. **模组加载源是 `ugc_mods/<cluster>/<shard>/content/322330/<id>`**；放 `mods/workshop-<id>` 会被游戏当 UGC 删掉。
3. **新世界必须先有 Steam UGC 记录**（`ugc_mods/<cluster>/<shard>/appworkshop_322330.acf` + `downloads/`），从已有世界 rsync 复制，否则首启动疯狂重下/失败（`[Workshop] ODPF failed entirely`）。
4. **世界生成参数写 `worldgenoverride.lua`**，绝不手写 `leveldataoverride.lua`（游戏内部完整关卡定义，手写必崩 `Level data override is invalid!`）。改完删 `save` 才生效。
5. **无 RCON**：命令只能写进程 stdin（PTY）+ `[MISC] console_enabled=true`；踢/封用 `TheNet:Kick/Ban/BanForTime/Unban`，不是 `c_kick`/`c_ban`。
6. **端口必须在安全组放行区间内**（游戏 UDP 10998–11018、Steam 查询 27015–27020、认证 8766–8770）；用显式槽位分配，别用公式推算。

## 1. 服务器初始化（一次性）

```bash
# 1) 32 位运行库（32 位 nullrenderer 必需）
dpkg --add-architecture i386 && apt-get update
apt-get install -y lib32gcc-s1 lib32stdc++6 libstdc++6:i386 libcurl4-gnutls-dev:i386 \
                   ca-certificates curl unzip tar rsync tmux file

# 2) swap（4 GiB 内存跑双分片必须）
fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab

# 3) 游戏用户 + steamcmd
useradd -m -s /bin/bash dst
su - dst -c 'mkdir -p ~/steamcmd && cd ~/steamcmd && \
  curl -sqL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz | tar zxf -'
su - dst -c '/home/dst/steamcmd/steamcmd.sh +force_install_dir /home/dst/dst-server \
  +login anonymous +app_update 343050 validate +quit'
```

验证：`<install>/bin64/` 下有 `dontstarve_dedicated_server_nullrenderer_x64`；`version.txt` 有 build 号。

## 2. 存档与集群配置

目录：`/home/dst/.klei/DoNotStarveTogether/<Cluster>/`

```
cluster.ini            [GAMEPLAY] [NETWORK] [MISC] [SHARD]
cluster_token.txt      Klei token（accounts.klei.com 生成）
adminlist.txt          每行一个 KUID（如 KU_xxxx）
Master/server.ini      server_port=10999, is_master=true, 27016/8766
Master/worldgenoverride.lua   世界生成参数（唯一合法入口）
Caves/server.ini       server_port=10998, is_master=false, name=Caves, id=2, 27017/8767
```

关键字段：`[SHARD] shard_enabled=true / master_ip=127.0.0.1 / master_port=10889 / cluster_key=<同一集群内一致> / bind_ip=127.0.0.1`。

## 3. systemd 单元（两个分片各一个）

```ini
[Service]
User=dst
Environment=HOME=/home/dst
WorkingDirectory=/home/dst/dst-server/bin64          # ★ 必须是 bin64
ExecStart=/home/dst/dst-server/bin64/dontstarve_dedicated_server_nullrenderer_x64 \
          -console -cluster <Cluster> -shard Master
Restart=on-failure
RestartSec=10
LimitNOFILE=8192
```

就绪判据：日志 `Online Server Started on port:`；空服 `Sim paused`，有人进变 `Sim unpaused`。

## 4. 模组（最容易翻车）

1. 清单：`/home/dst/mods.list`，行 `mod:<id>` 或 `collection:<id>`，行尾 `:disabled` 表示停用。
2. 每次更新都要**重写** `<install>/mods/dedicated_server_mods_setup.lua`（`ServerModSetup("id")` / `ServerModCollectionSetup("id")`），因为 `app_update validate` 会还原它。
3. 下载：`steamcmd +force_install_dir <install> +login anonymous +workshop_download_item 322330 <id> validate +quit`，成功标志 `Success. Downloaded item`，落点 `<install>/steamapps/workshop/content/322330/<id>`。
4. 同步：`rsync -a --delete <下载落点>/ <install>/ugc_mods/<cluster>/<shard>/content/322330/<id>/`，**每个分片一份**，先 `chown -R dst:dst`。
5. 启用：`<cluster>/<shard>/modoverrides.lua` → `return { ["workshop-378160973"] = { enabled = true, configuration_options = { ... } } }`；**每个分片各写一份**。
6. 加载校验：`journalctl -u dst-server | grep -c 'Registering Mod workshop-'` + `ModIndex: Load sequence finished successfully`。
7. 大改模组**不要一次装一堆**：会进入"报错→禁用→重载"死循环（实测单模组被禁用 3 万次、load 68、SSH 都登不上）。先装工具类，一次加一个，加完 `grep -c 'Disabling workshop'` 必须为 0。

## 5. 世界规则/生成（数据从游戏脚本拿，别手抄）

- 解包 `<install>/data/databundles/scripts.zip`（本机 Steam 客户端同路径也有）。
- 键名/默认值/适用世界：`scripts/map/customize.lua`；中文：`scripts/languages/chinese_s.po`；游戏风格与预设：`scripts/map/levels/forest.lua` / `caves.lua`。
- 标签规则：分组 `STRINGS.UI.SANDBOXMENU.WORLDSETTINGS_*`；设置项 `STRINGS.UI.CUSTOMIZATIONSCREEN[string.upper(name)]`；选项文本来自 `desc` 选项集。
- 解析两个坑：① 选项集在 `if IsNotConsole()` 两个分支里重定义，**取第一次出现**（否则"野火"只剩默认值）；② `world={"forest"}` 没空格，正则用 `\bworld\s*=\s*\{([^}]*)\}`。
- 写盘：`worldgenoverride.lua` 里 `{ override_enabled = true, overrides = {...}, preset = "SURVIVAL_TOGETHER" }`；删 `save` 重启后生效。

## 6. 备份与救火

- 备份：tar 整个集群目录到 `/home/dst/backups/<Cluster>-<stamp>.tar.gz`，`OnCalendar=*-*-* 04:30:00`、保留 7 份。
- 救火顺序：`systemctl stop` 先救内存 → `journalctl -u dst-server -n 300` 找触发模组 → 回滚备份 → 逐个加回。
- 内存经验：2C4G 跑双分片 + 9 工具模组约 3.1/3.5 GB（89%）；加内容模组前升 4C8G。

## 7. 接入 Campfire 面板（可选）

- 服务器：`campfire-agent` systemd 常驻，监听 `/run/campfire/agent.sock`（`RuntimeDirectory=campfire`，`srw-rw---- root:campfire`），把 SSH 用户加入 `campfire` 组。
- 本机：`campfire-panel.exe` 只监听 `127.0.0.1:8717`，通过 `x/crypto/ssh` 的 `direct-streamlocal` 连 socket；配置在 `%APPDATA%\campfire\panel.json`。
- `known_hosts` 必须包含服务器**全部**主机公钥（dss/ecdsa/ed25519/rsa），LF、无 BOM —— 否则 Go 的 `knownhosts` 报 `key mismatch`。

## 8. 每次交付前的验收（照抄）

```bash
systemctl is-active dst-server dst-server-caves campfire-agent
ss -lunp | grep -E '10999|10998|27016|27017|8766|8767'
journalctl -u dst-server -n 300 --no-pager | grep -E 'Online Server Started|World generated|Disabling workshop'
ls -lt /home/dst/backups | head -5
```

```powershell
Invoke-RestMethod http://127.0.0.1:8717/api/overview
Invoke-RestMethod http://127.0.0.1:8717/api/clusters
```

## 9. 操作纪律（血泪）

- 动服务器前先备份；动模组前先看有没有人在线。
- 不熟的 DST 行为，解包脚本读源码，别猜。
- 长脚本一律"本地写 .sh → scp → 远程 bash"，不要用 PowerShell here-string 拼 ssh 命令（会被压平/吞引号）；文件用 UTF-8 **无 BOM + LF**。
- `pkill -f` 会匹配到 ssh 自身命令行把自己的会话杀掉，改用显式 PID。

## 10. 先分析原因 → 给方案 → 用户拍板 → 再执行（用户强制要求）

用户（项目所有者）2026-11-02 明确要求：**「以后叫你解决问题、叫你做什么东西，都要先分析原因，给出方案，再由我决定以什么方案执行。」**

- **可以直接做**：只读取证（读代码/日志/配置、`journalctl`、`ps`、`ss`、`free`、`tcpdump` 只抓包、`GET` 接口、`tar` 备份、grep 统计）。
- **必须先批准**：改代码/配置、启停重启服务、装删更新模组、改世界规则/重新生成/回档、删存档、动 sshd/安全组/证书/隧道/端口、任何批量组合操作。
- **三段式输出**：① 原因（现象 + 可复核证据 + 机制 + 影响面 + 置信度）；② 方案 2–4 个（做法/代价/风险与回滚/耗时与用户配合/推荐理由）；③ 明确问「选哪个」并停下等回答。
- **紧急例外**：服务器已宕机或数据可能损坏时，可先做「只读取证 + 最小恢复动作」，但必须同一条消息说明原因与后续方案。
- 执行后必须给：做了什么 / 验证证据 / 回滚方法 / 剩余事项。
- 细则见本机 skill：`D:\DeepseekHarness\dsh-home\skills\analyze-first-approve-before-execute\SKILL.md`。
