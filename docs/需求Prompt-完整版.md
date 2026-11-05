# 需求 Prompt（完整版，以最后一次确认为准）

> 用途：把这个文件整段贴给一个新的 agent / 新对话，它就能从零把这个东西做出来（或接着做）。
> 版本：2026-11-01 v3（并入"创建世界 / 单个模组更新 / 模组介绍页"三项追加需求）。
> 凡出现 `<<...>>` 的地方是需要替换的参数，清单见 `docs\通用化与替换点.md`。

---

## 0. 你的角色与最终目标

你是我的开发与运维伙伴。最终目标：让我（一个人）能在自己的 Windows 电脑上，用一个小管理平台远程管理一台国内云服务器上的《饥荒联机版》（Don't Starve Together）专用服务器，供我和朋友联机；创意工坊模组能一键更新、也能单个更新；能创建/切换多个世界并设置世界生成规则。

**一句话验收**：我双击桌面图标 → 浏览器里能看到服务器状态、能看到/管理 9 个以上模组（含详情页与更新）、能创建一个带自定义世界规则的新世界并开服，朋友能进来。

## 1. 已确认的前提（不要再问，直接照做）

| 项 | 决定 |
|---|---|
| 服务器 | 阿里云 ECS，国内（华东1 杭州），Ubuntu 22.04 LTS，2C4G 起（6–8 人双分片建议 4C8G） |
| 网络 | 私服、国内；公网 IP 直连，不需要域名/备案；面板**只在本人电脑**用 |
| 面板运行位置 | **我自己的 Windows 电脑**，通过 SSH 管理云服务器（面板不部署到服务器上） |
| 用户 | **单用户**（我自己）；朋友只玩不登录面板；多用户/外网访问是以后的事，本次不做但要留结构 |
| 客户端形态 | 桌面双击启动的程序（Go 单二进制 + 内嵌 Web UI，本机 127.0.0.1 访问）；不做微信小程序 |
| 服务器常驻 | 面板关掉、我电脑关机，服务器照跑（服务端 systemd 自启 + 面板只是遥控器） |
| 一次性来源 | 不 fork 现成开源面板（dst-admin / dst-admin-go 等），自研，但架构可参考 |

## 2. 明确不做（本次范围外）

- 微信小程序 / 手机端；多用户与权限矩阵；邀请制登录；公网 HTTPS + 2FA（结构预留即可）。
- Windows 服务器部署（Linux 优先）；Docker 化；多台云服务器同时管理。
- 自动化测试体系、CI/CD（先把功能跑通，测试后续补）。

## 3. 架构（已定的技术方案）

```
[Windows] campfire-panel.exe (Go + 内嵌 Web UI, 监听 127.0.0.1:8717)
      │  SSH (golang.org/x/crypto/ssh, direct-streamlocal)
      ▼
[云服务器] campfire-agent (Go, systemd 常驻, unix socket /run/campfire/agent.sock, root:campfire 0660)
      │  行 JSON-RPC
      ▼
  DST 服务端（steamcmd 装 appid 343050）+ systemd 单元 + 集群配置 + ugc_mods
```

- 面板与 Agent 之间：**HTTP/JSON 行协议**，op 名 + params，返回 `{id, ok, result|error}`。
- 重活（steamcmd 下载、rsync、备份、重启、等 `Online Server Started`）都在 Agent 侧做，任务化（Job：`running/done/failed` + 日志数组），面板轮询显示进度。
- 面板只监听 `127.0.0.1`，带 Host/Origin 校验。

## 4. 功能需求

### 4.1 服务器 / 世界（P0）

1. **世界列表**：显示每个世界（集群）的名称、游戏风格、天数/分片（森林和洞穴）、端口、运行状态；操作：启动、停止、设为当前、删除（删除前自动 tar 备份到 `/home/dst/backups/`）。
2. **创建世界（核心，要照游戏内创建流程做）**，四步向导：
   - 设置：世界名（`^[A-Za-z0-9_-]{3,20}$`，同时作为目录名）、描述、**游戏风格**（轻松/无尽/生存/荒野/暗无天日，取游戏 `AddPlaystyleDef` 的官方中文名与说明）、最多玩家、PvP、是否开洞穴、密码；
   - 地上规则：显示**全部地上世界规则/世界生成设置**（从游戏脚本自动生成，见 4.3），可选预设；
   - 洞穴规则：同上，只显示洞穴可用项；
   - 创建：写 `cluster.ini` / `server.ini` / `worldgenoverride.lua` → 复制 Klei Token 与 `adminlist/whitelist/blocklist` → 装两个 systemd 单元 → 同步模组 → 可选立即启动。
3. **同一时间只跑一个世界**（2C4G 内存不够），启动新世界前自动停其它世界，并在界面上说明原因。
4. 端口必须落在云安全组已放行区间（见 4.6），用"槽位分配"而不是公式推算。

### 4.2 模组管理（P0）

1. 列表：缩略图、名称、`workshop-<id>`、版本、体积、订阅数、是否"朋友必装"、是否需要更新、启用开关。
2. **一键更新全部**：写 `dedicated_server_mods_setup.lua` → steamcmd 批量下载 → 同步到分片 ugc → 写 `modoverrides.lua` → 备份 → 重启 → 等就绪。
3. **单个模组更新**：只下载/同步这一个模组，其余不动；可选是否立即重启。
4. **模组介绍页**：大图、标题、作者、版本、更新时间、体积、订阅、官方简介（去 BBCode）、标签、每个分片的启用状态、跳转工坊页面；有 `configuration_options` 的模组可在此直接改设置。
5. **从 Steam 创意工坊添加**：粘贴链接或 ID → 服务器侧查 Steam API 显示标题/简介/体积，确认后下载安装；支持合集展开。
6. 逐模组设置：读写 `modoverrides.lua`（**每个分片一份**），保留已有配置项。
7. 移除模组：从清单移除 + 清理 ugc + 写 overrides + 重启。

### 4.3 世界规则目录（P0，必须自动生成，不许手抄）

- 数据源：本机 Steam 客户端 `Don't Starve Together/data/databundles/scripts.zip` 里的
  `scripts/map/customize.lua`（分组/键名/默认值/`world`/选项集）、`scripts/languages/chinese_s.po`（中文）、
  `scripts/map/levels/forest.lua` 与 `caves.lua`（游戏风格与预设 ID）。
- 产出：`worldgen_catalog.json`（`go:embed` 打包），含 `groups[]{name, kind(settings|worldgen), settings[]}` + `playstyles[]` + `presets[]`；
  规模参考：14 组 / 214 项 / 5 风格 / 7 预设。
- 写文件：地上/洞穴各一个 `worldgenoverride.lua`：
  ```lua
  -- 由 Campfire 管理，手动修改会在面板里显示为"外部修改"
  return { override_enabled = true, overrides = { wildfires = "never" }, preset = "SURVIVAL_TOGETHER" }
  ```
- **绝对不要写 `leveldataoverride.lua`**（那是游戏内部完整关卡定义，手写必然崩）。

### 4.4 概览与日志（P0）

- 概览：各分片 active/端口/重启次数、内存/swap/磁盘、模组数、是否有人在线、游戏版本、Agent 版本。
- 日志：查看某个服务的最近日志；任何长任务都能看到逐行日志。

### 4.5 后续（本次可只留接口/结构）

玩家管理（在线列表、踢人、封禁/解封、管理员名单）、存档回档（`max_snapshots` 快照 + 一键回滚）、Windows 托盘与开机自启。

### 4.6 云服务器与端口（已定）

- 安全组入方向：UDP `10998–11018`（游戏，玩家连的是 `10999`）、`27015–27020`（Steam 查询）、`8766–8770`（Steam 认证）→ `0.0.0.0/0`；TCP `22` → `0.0.0.0/0`（仅密钥登录 + fail2ban）。
- 不要考虑把 22 锁到我的 IP（移动网络出口 IP 会变，锁了只能走控制台救）。

## 5. 技术硬约束（违反必炸，都是真机踩出来的）

1. **无 RCON**：想执行游戏命令只能往进程 stdin 写（PTY），且要 `[MISC] console_enabled=true`；踢/封是 `TheNet:Kick(userid)` / `TheNet:Ban(userid)` / `TheNet:BanForTime(userid,sec)` / `TheNet:Unban`，**不是** `c_kick`/`c_ban`。
2. **服务端进程的工作目录必须是 `<install>/bin64`**，否则 `Error, failed to change working directory to ../data`。
3. **模组内容加载源是 `ugc_mods/<cluster>/<shard>/content/322330/<id>`**，不是 `mods/workshop-<id>`（后者会被游戏当 UGC 删掉）。
4. **新世界要有 Steam UGC 记录**（`appworkshop_322330.acf` + `downloads/`），否则首启动会重下模组甚至失败；从已有世界复制。
5. **世界生成参数只在生成时生效**：改完必须删/改名 `save` 重新生成；`worldgenoverride.lua` 是唯一合法入口。
6. **工坊没有历史版本**：任何更新前先备份存档；大改模组互相冲突会进入"报错→禁用→重载"死循环，要能快速停服。
7. **`app_update 343050 validate` 会重置 `dedicated_server_mods_setup.lua`**，所以每次更新后都要重写它。
8. 服务器端 32 位运行库必需（`i386` 架构 + `lib32gcc-s1/lib32stdc++6/libstdc++6:i386/libcurl4-gnutls-dev:i386`）。
9. Steam Web API：`ISteamRemoteStorage/GetPublishedFileDetails/v1`（POST，匿名可用）与 `GetCollectionDetails/v1`；`file_size/time_updated/subscriptions` 是**字符串**；`IPublishedFileService/QueryFiles` 需要 key，别用。

## 6. 交付物

1. `platform/` Go 工程（module `campfire`）：`cmd/campfire-agent`、`cmd/campfire-panel`、`internal/{agent,panel,proto,luaconf}`、内嵌 `web/`。
2. `platform/bin/campfire-panel.exe`（本机）+ `campfire-agent-linux`（服务器）。
3. 部署脚本/说明：Agent 装成 systemd；面板配 `%APPDATA%\campfire\panel.json`。
4. 桌面快捷方式"篝火面板"（图标自绘）。
5. 文档：设计文档、README、踩坑记录、交接说明。

## 7. 验收标准（逐条可验）

1. 打开面板：概览显示两个分片 active、9 个以上模组、内存/磁盘数值正确。
2. 模组：点"详情"能看到大图与简介；点"更新"单个模组能跑完并显示日志；"一键更新"能跑完。
3. 世界：点"创建新世界"，四步选完能创建成功（落盘 `cluster.ini/server.ini/worldgenoverride.lua` 正确、端口在安全组区间），能启动并进入游戏。
4. 世界规则：把"野火"设为"从不"→ 创建/重新生成后进游戏生效；改回"默认"也生效。
5. 删除世界：目录与 systemd 单元被清理，且备份留在 `/home/dst/backups/`。
6. 服务器重启后：`dst-server` / `dst-server-caves` / `campfire-agent` 自动起来，面板能连上。

## 8. 环境参数（替换点，见 `docs\通用化与替换点.md`）

- 云服务器公网 IP：`<<SERVER_IP>>`（当前 `203.0.113.10`）
- SSH 用户/端口：`<<SSH_USER>>`（`ops`）/ `<<SSH_PORT>>`（22）
- 私钥：`<<KEY_PATH>>`（`<SSH_KEY_PATH>`）
- 集群名：`<<CLUSTER_NAME>>`（`MyDediServer`）；房间名/密码：`<<WORLD_TITLE>>` / `<<WORLD_PASSWORD>>`（Xomoyo / CHANGE_ME_CLUSTER_PASSWORD）
- 安装目录：`<<INSTALL_DIR>>`（`/home/dst/dst-server`）；游戏用户：`<<GAME_USER>>`（`dst`）
- Steam 64 位 ID / Klei ID（管理员）：`<<STEAM_ID>>`（7656119XXXXXXXXXX）/ `<<KLEI_ID>>`（KU_REPLACE_ME）

## 9. 工作方式要求

- 每一步都要**真机验证**，不要只写代码就说完成；把验证命令与输出摘要一起汇报。
- 改动前后保证"原有世界不被打坏"：动服务器前先备份，动模组前先看有没有人在线。
- 遇到不确定的 DST 行为，**解包游戏脚本读源码**（`data/databundles/scripts.zip`），不要凭记忆猜。
- 每次会话结束前更新 `docs\交接说明.md`。
