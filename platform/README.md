# Campfire · 饥荒联机版（DST）服务器管理面板

本机 Windows 上跑一个面板，通过 SSH 管理阿里云 ECS 上的 DST 专服。面板只监听 `127.0.0.1`，不对外暴露。

```
你电脑（Windows）                                    阿里云 ECS（Ubuntu 22.04）
┌──────────────────────────┐   SSH(22) 隧道    ┌────────────────────────────────────┐
│ campfire-panel.exe       │ ───────────────▶ │ campfire-agent (systemd)           │
│  · 127.0.0.1:8717 网页   │  unix socket      │  /run/campfire/agent.sock          │
│  · 内嵌 HTML/CSS/JS      │ ───────────────▶ │  ├─ SteamCMD / Steam Web API        │
└──────────────────────────┘                   │  ├─ /home/dst/.campfire/clusters.json│
                                                │  └─ dst-<世界>-master / -caves      │
                                                └────────────────────────────────────┘
```

- 面板负责界面与编排；服务器上的 Agent 常驻（systemd），**电脑关机服务器照跑**。
- 多世界：每个世界自己一套 `cluster.ini/server.ini/systemd 单元/ugc 模组目录`，同一时间只跑一个（2C4G 内存只够一份）。
- `mods.list`（`/home/dst/mods.list`）是"启用了哪些模组"的唯一事实来源，面板与命令行工具共用。

## 一、启动面板

```powershell
# 第一次：带上连接参数，会自动保存到 %APPDATA%\campfire\panel.json
E:\gamedeepseek\platform\bin\campfire-panel.exe `
  -host 203.0.113.10 `
  -ssh-user ops `
  -ssh-key <SSH_KEY_PATH> `
  -known-hosts E:\gamedeepseek\secrets\known_hosts

# 之后：双击 start-panel.cmd（面板在独立窗口里跑，关窗口即停），或
E:\gamedeepseek\platform\bin\campfire-panel.exe
```

浏览器打开 <http://127.0.0.1:8717>。深链接：`/#worlds`、`/#mods`、`/#worldgen`、`/#logs`；另外 `?mod=<id>#mods` 直接打开模组详情、`?newworld=1#worlds` 直接打开创建世界向导。

## 二、功能（当前版本 0.2.9）

| 页面 | 能做什么 |
| --- | --- |
| 概览 | 当前世界两个分片（Master/Caves）运行状态、重启次数、内存/磁盘/swap、模组数、在线状态、游戏 build、重启分片 |
| 世界 | 世界列表（标题、目录、游戏模式、人数、端口、Steam 端口、分片状态）；启动/停止/设为当前/删除（删除前自动备份存档）；**创建新世界**四步向导 |
| 创建世界向导 | ① 设置：名称、描述、游戏风格（轻松/无尽/生存/荒野/暗无天日，带官方说明）、最多玩家、PvP、是否开洞穴、密码 ② 地上规则 ③ 洞穴规则 ④ 确认创建（可选立即启动、复制当前模组） |
| 模组管理 | 模组卡片（缩略图/标题/版本/体积/订阅数/是否"朋友必装"/是否有新版本）；启用停用；**单个模组更新**；**一键更新全部**；逐模组设置；移除；从 Steam 创意工坊粘贴链接添加（支持合集） |
| 模组详情 | 抽屉页：大图、workshop-id、版本、体积、订阅数、作者、官方简介（去 BBCode）、tags、更新时间、各分片是否启用、模组设置项、"更新这个模组"、"在工坊页面打开" |
| 世界生成 | 按分片（地上 Master / 洞穴 Caves）编辑 `worldgenoverride.lua`；设置目录由游戏 `customize.lua` + 中文语言包自动生成（14 组 / 214 项）。**地上页与洞穴页是两套不同的设置集**：agent 按 `customize.lua` 的 `world` 字段给每项算出 `scope`（forest/cave/both/global）与 `phase`（generation/rules），无 `world` 字段的全局规则（季节/昼夜/死亡复活/节日/坎普斯）只归地上主控；页面按「世界生成设置 · 生成后锁定」→「世界规则设置 · 运行时可改」→「全局规则 · 森林主控 · 自动同步到洞穴」三段渲染；**保存时按分片过滤**不属于该分片的项并返回 `dropped`；与默认值相同的不写入；**预设按分片校验**（洞穴必须是 `DST_CAVE*`，缺预设/类型不符会在页面上红灯告警并给"一键改为 DST_CAVE 并保存"；森林 location 的残留键 `task_set/start_location/roads...="default"` 会自动清除）；**"重新生成"可只重生当前分片**（另一分片存档不动），生成前预检、生成后按 `shardindex` 的 `location` 后验；地上页还有「清理多枝树（一次性）」按钮（见下） |
| 服务器控制台 | `POST /api/console/exec {cluster, shard, command}`：DST 没有 RCON，agent 通过 `campfire-shard-run` 包装器把命令写进分片 stdin（`tail -F cmd | server -console`），再用 `CFM-/CFD-` marker + `server_log.txt` 的 offset 增量回读输出（过滤回显行、25s 超时）。世界生成页「清理多枝树（一次性）」= `c_removeall("twiggytree")` + `c_save()`（「多枝树再生=无」只管再生，初始数量游戏无开关） |
| 历史存档 · 回档 | 每个分片保留最近 2 份 `save-*` 快照（重新生成世界时旧存档自动变成快照）；表格显示时间/世界类型/会话/大小，「回档到此」= 备份 → 停两片 → 当前存档留 `pre-restore-*` → 复制快照 → 按「先地上后洞穴」重启并等握手 → 后验 `location`；「立即创建快照」手动留点 |

## 三、后端接口速查（`127.0.0.1:8717`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/health` | Agent ping |
| GET | `/api/overview` | 分片/资源/模组数 |
| GET | `/api/clusters` | 世界列表 + 当前激活世界 |
| POST | `/api/clusters/create` | 任务：创建世界（`CreateWorldParams`） |
| POST | `/api/clusters/start` / `stop` / `remove` | `{name}` |
| POST | `/api/clusters/activate` | `{name}` 设为当前世界 |
| GET | `/api/mods?cluster=<世界>` | 模组清单（含工坊标题、设置项与当前值、needs_update） |
| POST | `/api/mods/detail` | `{cluster,id}` 模组详情 |
| POST | `/api/mods/update` | 任务：更新全部模组 |
| POST | `/api/mods/updateOne` | 任务：`{cluster,id,restart}` 只更新一个模组 |
| POST | `/api/mods/enable` | `{id, enabled}` |
| POST | `/api/mods/config` | `{id, options}` |
| POST | `/api/mods/install` | `{ids:[...]}` 或 `{collections:[...]}` |
| POST | `/api/mods/remove` | `{id}` |
| POST | `/api/workshop/lookup` | `{input}` 工坊链接/ID |
| GET | `/api/image?url=` | 代理 Steam 图片（域名白名单） |
| GET/POST | `/api/worldgen?cluster=&shard=Master` | 读/写 `worldgenoverride.lua` + 设置目录（目录每项带 `scope`/`phase`/`shards`；保存返回 `{ok, dropped}`） |
| POST | `/api/worldgen/regenerate` | 任务：`{cluster, shard}`（`shard` 为空=全部分片） |
| POST | `/api/console/exec` | 在分片控制台执行一条命令：`{cluster, shard, command}` → `{shard, command, lines}`；分片必须是新启动方式（包装器），首次需重启一次分片 |
| GET | `/api/worldgen/snapshots?cluster=` | 历史存档列表（`{cluster, keep, snapshots:[...]}`） |
| GET | `/api/logs?service=&lines=` | 日志 |
| GET | `/api/jobs/<id>` | 任务状态与日志 |

## 四、Agent 部署（服务器侧，一般不用重跑）

```bash
# 本机
scp -i <SSH_KEY_PATH> platform/bin/campfire-agent-linux ops@<IP>:/tmp/campfire-agent
scp -i <SSH_KEY_PATH> deploy/install-agent.sh ops@<IP>:/tmp/install-agent.sh
ssh -i <SSH_KEY_PATH> ops@<IP> "sudo bash /tmp/install-agent.sh"
```

`install-agent.sh` 会：建 `campfire` 组并把 `ops` 加进去、装二进制到 `/usr/local/bin/campfire-agent`、写 `/etc/systemd/system/campfire-agent.service`（`Restart=always`）、启动并输出 socket 权限（`srw-rw---- root campfire /run/campfire/agent.sock`）。

## 五、世界与端口

- 世界注册表：`/home/dst/.campfire/clusters.json`（记录标题、端口、systemd 单元名；删掉也会从磁盘重新发现）。
- 端口分配必须落在阿里云安全组已放行的范围：游戏 `10998–11018`、Steam 查询 `27015–27020`、认证 `8766–8770`；分片内部通信 `10889+`（只监听 127.0.0.1）。
- 新世界会自动复制 `cluster_token.txt / adminlist.txt`，复制当前模组（含 Steam UGC 记录 `appworkshop_322330.acf` + `downloads/`），否则首启动会重新下载全部模组。

## 六、踩过的坑（都已在代码/文档里处理）

1. **known_hosts 必须覆盖服务器所有主机密钥**：Ubuntu 的 sshd 会先给 `ecdsa-sha2-nistp256`，而 Go 的 `knownhosts` 只认同类型的条目——只写 `ssh-ed25519` 就会报 `ssh: handshake failed: knownhosts: key mismatch`（OpenSSH 客户端没事，因为它会挑 ed25519）。生成方法：
   ```bash
   ssh ops@<IP> 'sudo bash -c '"'"'for f in /etc/ssh/ssh_host_*_key.pub; do echo "<IP> $(cat $f)"; done'"'"'' > known_hosts
   ```
   另外文件必须是 **LF** 换行、**无 BOM**。
2. **DST 会在启动时把 `KLEI     1 ` 这个魔数头写进配置/存档 Lua 文件**，解析 `worldgenoverride.lua`、`modoverrides.lua` 前要先跳过它（`luaconf.stripKleiHeader`）。
3. **Steam Web API 的 `file_size` / `time_updated` / `subscriptions` 是字符串**，直接 unmarshal 到 int64 会报错（`flexInt64` 处理）。
4. **`leveldataoverride.lua` 不能手写**（游戏内部完整关卡定义），用户可写的是 `worldgenoverride.lua`；详见设计文档 §5.3.2。
5. 国内网络访问 `api.steampowered.com` / 工坊图片偶发超时，已加重试 + 6 小时缓存 + 图片代理。
6. 写文件一律 UTF-8 **无 BOM**；PowerShell 用 `Set-Content -Encoding UTF8` 会带 BOM，导致 shebang/JSON 解析失败。
7. 世界生成设置的选项集在 `customize.lua` 里被 `if IsNotConsole()` 分成两支，取**第一支**（客户端分支，选项最全）；`autodetect` 这类表名不带 `_descriptions` 后缀，不能只匹配 `_descriptions`。

## 七、代码结构

```
platform/
├─ cmd/campfire-agent/        服务器侧守护进程（linux）
├─ cmd/campfire-panel/        本机面板（windows）
├─ internal/agent/            RPC 分发、世界、模组、世界生成、工坊、系统信息
│   ├─ agent.go  clusters.go  mods.go  worldgen.go  catalog.go  workshop.go  console.go  snapshots.go  exec.go  sysinfo*.go
│   ├─ worldgen_catalog.json  ← tools/gen_worldgen_catalog.py 生成（14 组 / 214 项 / 5 风格）
├─ internal/panel/            HTTP + SSH direct-streamlocal 隧道 + 内嵌前端
│   ├─ rpc.go  http.go  web/{index.html,style.css,app.js}
├─ internal/luaconf/          Lua 子集解析/渲染（含 KLEI 头处理）
├─ internal/proto/            RPC 数据结构
└─ tools/                     gen_worldgen_catalog.py / peek_*.py + wg/（从 scripts.zip 解出的游戏脚本）
```

## 七点五、临时公网链接（给别人看）

面板默认只监听 `127.0.0.1:8717`，并且有 Host 校验（防 DNS rebinding），所以外网直接打不开。需要临时给别人演示时：

1. 双击 `start-public-link.cmd` → 窗口里会打印一行 `https://xxxx.lhr.life`，这就是公网链接
2. 把链接发给对方；**保持这个窗口开着**，关掉窗口链接就断
3. 每次重启窗口链接都会变（免费临时隧道，没有固定域名）

原理：`start-public-link.cmd` 用系统自带 `ssh` 做反向隧道（`-R 80:127.0.0.1:8717 nokey@localhost.run`），localhost.run 返还一个 https 域名；面板侧通过 `%APPDATA%\campfire\panel.json` 的 `"public_hosts": ["*.lhr.life", ...]`（或启动参数 `--public-host`）放行该域名，Host 校验只放行「本机 + 这些域名」（实现见 `internal/panel/http.go` 的 `hostGuard`/`hostAllowed`）。

公网访问现在有两道保护（0.2.6，`panel.json` 里配置）：

| 配置 | 作用 |
|---|---|
| `"auth_user"` / `"auth_pass"` | 非本机访问必须 HTTP Basic 登录（浏览器弹框）；`auth_pass` 为空且配了 `public_hosts` 时启动自动生成并写回配置，启动日志会打印。本机 `127.0.0.1` 免登录，日常使用不受影响 |
| `"read_only": true` | 登录后的公网用户只能看：所有写操作（停服、改世界、装模组、回档、控制台）返回 403「只读演示模式」；本机仍为完整权限 |

另外：同 IP（隧道下都表现为 127.0.0.1）连续 5 次登录失败会锁 10 分钟，防止被暴力试口令。改口令：编辑 `%APPDATA%\campfire\panel.json` 的 `auth_pass`，或用启动参数 `--auth-pass <新口令>`、`--read-only=false` 关闭只读。

> 试过但不可用的方案：Cloudflare trycloudflare 快速隧道（`api.trycloudflare.com` 的 POST 直接超时，被墙）；localtunnel（浏览器访问会被 511「Tunnel password」拦）；bore（exe 被 Windows Defender 拦）。固定地址最后用的是「阿里云 Caddy + SSH 反向隧道」，见下面的七点六。
## 七点六、固定公网入口（0.2.7，长期可看）

上面那种临时隧道每次重连都会换域名。要一个**固定地址、长期可用、证书有效**的入口，现在走「阿里云 Caddy + 这台电脑的 SSH 反向隧道」：

```
浏览器 ──https──> dst-demo.example.com（阿里云 Caddy，443）
                        │  reverse_proxy 127.0.0.1:18717
                        ▼
              SSH 反向隧道（本机 ssh -R 18717:127.0.0.1:8717）
                        ▼
              本机面板 127.0.0.1:8717
```

- **固定地址**：`https://dst-demo.example.com` —— sslip.io 会把 `203-0-113-10` 解析回 `203.0.113.10`，所以**不用买域名**
- **证书**：Caddy 自动向 Let's Encrypt 申请并续期（存储在 `/var/lib/caddy`），浏览器不报警；`http://203.0.113.10` 会 301 跳到 https 域名
- **分工**：面板和隧道跑在这台电脑（`start-panel.cmd` + `start-fixed-link.cmd`，后者已注册登录自启任务 `CampfireFixedLink`）；Caddy 跑在阿里云（systemd `caddy.service`，已 enable 开机自启）
- **安全**：沿用 0.2.6 的两道保护——公网必须 Basic 登录（`auth_user`/`auth_pass`），`read_only: true` 时只能看；本机 `127.0.0.1` 免登录、完整权限
- **前提**：安全组入方向放行 TCP `80` 与 `443`；这台电脑要开机，面板与隧道窗口要开着

维护命令（阿里云上）：

```bash
sudo systemctl status caddy          # 运行状态
sudo journalctl -u caddy -n 50       # 证书/反代日志
sudo systemctl restart caddy         # 改过 /etc/caddy/Caddyfile 后重启
```

服务器配置文件 `/etc/caddy/Caddyfile`（反代目标 `127.0.0.1:18717`）。注意：Caddy 的访问日志**不要**再写 `/var/log/caddy/*.log`——systemd 沙箱下会 `permission denied` 起不来，日志走 journald 即可。

## 七点七、直连模式（`offline_cluster`，0.2.8）

当 Klei 服务器列表返回 `E_ROWID_EXIST`（上一个同 Token 的登记行没释放）时，Master 会一直注册不上，洞穴也因此连不上。这时把世界切成「不登记公共列表」：

```ini
[NETWORK]
offline_cluster = true
```

- Master 30 秒就绪、洞穴 60 秒握手；面板「重启当前世界」约 2 分 20 秒完成并自动验证握手；
- 代价：游戏内「浏览世界」列表搜不到，朋友用控制台 `c_connect("<公网IP>", 10999)` 进服；
- Agent 会自动识别这个模式（等 `Sim paused` 而不是 `Online Server Started on port:`），撞行时给中文提示并最多等 15 分钟；
- 想恢复列表：删掉这行重启；仍撞行就等 15 分钟或换一个新 Klei Token。

- **0.2.9 起「停止」不再是 `systemctl stop`**：Agent 通过服务器控制台发 `c_shutdown(true)`，游戏会存档并向 Klei 注销列表行，然后再启动——这才是「重启后还能被搜到」的关键（实测重启后 33–35 秒即注册，`E_ROWID_EXIST` 计数 0）。

```
# 判断当前是否被强杀过（会留下列表行 → 重启撞 E_ROWID_EXIST）
sudo journalctl -u dst-server --no-pager | grep -i "shutting down\\|signal"
```

## 八、下一步（待做）

- 玩家管理（踢/封、管理员名单、在线玩家列表）——**控制台通道已经做好（`/api/console/exec`，见上）**，剩下是页面 UI 和 `TheNet:Kick/Ban` 命令封装。
- ~~存档回档~~ —— **已完成（0.2.4）**：每分片保留最近 2 份 `save-*` 快照，世界生成页可直接回档。
- 面板打包成单文件 exe + 托盘图标，开机自启。
- 世界生成页已把「世界生成」（地形/资源/巢穴，生成后锁定）与「世界规则」（季节/袭击/再生，运行时可改）都暴露出来，并单独分出一组「全局规则 · 森林主控 · 自动同步到洞穴」，只在地上页出现。
