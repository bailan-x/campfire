package agent

// 世界（cluster）管理：注册表、目录扫描、端口分配、systemd 单元、创建/启动/停止/删除。
//
// 一个「世界」= 一个 DST cluster 目录（~/.klei/DoNotStarveTogether/<name>），
// 由 1 或 2 个分片（Master、可选 Caves）组成，每个分片一个 systemd 单元。
// 注册表放在 <home>/.campfire/clusters.json，记录 active 世界与每个世界的
// 端口/单元名；启动某个世界时会先停掉其它世界（2C4G 跑不起两个）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"campfire/internal/proto"
)

// clusterDef is one world's persisted description.
type clusterDef struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Dir         string `json:"dir"`
	GameMode    string `json:"game_mode"`
	Description string `json:"description"`
	Password    string `json:"password"`
	MaxPlayers  int    `json:"max_players"`
	PvP         bool   `json:"pvp"`
	Caves       bool   `json:"caves"`
	MasterPort  int    `json:"master_port"`
	CavesPort   int    `json:"caves_port"`
	ShardPort   int    `json:"shard_port"`
	// Steam 查询与认证端口，必须落在安全组允许的范围内（见 README）。
	MasterSteamPort int    `json:"master_steam_port"`
	CavesSteamPort  int    `json:"caves_steam_port"`
	MasterAuthPort  int    `json:"master_auth_port"`
	CavesAuthPort   int    `json:"caves_auth_port"`
	MasterUnit      string `json:"master_unit"`
	CavesUnit       string `json:"caves_unit"`
	CreatedAt       int64  `json:"created_at,omitempty"`
}

func (c clusterDef) shards(cfg Config) []Shard {
	out := []Shard{{Name: "Master", Service: c.MasterUnit, Port: c.MasterPort}}
	if c.Caves {
		out = append(out, Shard{Name: "Caves", Service: c.CavesUnit, Port: c.CavesPort})
	}
	_ = cfg
	return out
}

type registryFile struct {
	Version  int          `json:"version"`
	Active   string       `json:"active"`
	Clusters []clusterDef `json:"clusters"`
}

const registryVersion = 1

func (a *Agent) regPath() string {
	return filepath.Join(a.cfg.Home, ".campfire", "clusters.json")
}

// loadRegistry reads the worlds registry, migrating a pre-existing install that
// only knows the single cluster from the command line flags.
func (a *Agent) loadRegistry() (*registryFile, error) {
	data, err := os.ReadFile(a.regPath())
	if err == nil {
		var rf registryFile
		if err := jsonUnmarshal(data, &rf); err == nil && rf.Clusters != nil {
			// 用磁盘上的 cluster.ini / server.ini 覆盖端口、名称、标题等派生字段，
			// 这样手改过配置文件、或从旧版本升级上来的世界也能正确显示。
			for i := range rf.Clusters {
				fresh := a.clusterFromDir(rf.Clusters[i].Dir)
				if fresh.Name == "" {
					continue
				}
				fresh.CreatedAt = rf.Clusters[i].CreatedAt
				if fresh.Title == "" {
					fresh.Title = rf.Clusters[i].Title
				}
				rf.Clusters[i] = fresh
			}
			return &rf, nil
		}
	}
	rf := &registryFile{Version: registryVersion, Clusters: a.discoverClusters()}
	for i := range rf.Clusters {
		if filepath.Clean(rf.Clusters[i].Dir) == filepath.Clean(a.cfg.ClusterDir) {
			rf.Active = rf.Clusters[i].Name
			break
		}
	}
	if rf.Active == "" && len(rf.Clusters) > 0 {
		rf.Active = rf.Clusters[0].Name
	}
	if err := a.saveRegistry(rf); err != nil {
		return rf, err
	}
	return rf, nil
}

func (a *Agent) saveRegistry(rf *registryFile) error {
	data, err := jsonMarshalIndent(rf)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.regPath()), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(a.regPath(), data, 0o644)
}

// discoverClusters lists cluster dirs (siblings of the configured cluster dir)
// and their basic settings, so existing hand-made worlds show up in the panel.
func (a *Agent) discoverClusters() []clusterDef {
	parent := filepath.Dir(a.cfg.ClusterDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var out []clusterDef
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(parent, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "cluster.ini")); err != nil {
			continue
		}
		out = append(out, a.clusterFromDir(dir))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// clusterFromDir reads a cluster dir into a clusterDef, filling unit names by
// convention (the legacy cluster keeps its original unit names).
func (a *Agent) clusterFromDir(dir string) clusterDef {
	name := filepath.Base(dir)
	c := clusterDef{
		Name:       name,
		Dir:        dir,
		GameMode:   "survival",
		MaxPlayers: 6,
		MasterPort: 10999,
		CavesPort:  10998,
		ShardPort:  10889,
	}
	if st, err := os.Stat(dir); err == nil {
		c.CreatedAt = st.ModTime().Unix()
	}
	ini := readINI(filepath.Join(dir, "cluster.ini"))
	if v := ini.get("GAMEPLAY", "game_mode"); v != "" {
		c.GameMode = v
	}
	if v := ini.get("GAMEPLAY", "max_players"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MaxPlayers = n
		}
	}
	c.PvP = ini.get("GAMEPLAY", "pvp") == "true"
	c.Password = ini.get("NETWORK", "cluster_password")
	c.Description = ini.get("NETWORK", "cluster_description")
	c.Title = ini.get("NETWORK", "cluster_name")
	if v := ini.get("SHARD", "master_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.ShardPort = n
		}
	}
	masterINI := readINI(filepath.Join(dir, "Master", "server.ini"))
	if v := masterINI.get("NETWORK", "server_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MasterPort = n
		}
	}
	if v := masterINI.get("STEAM", "master_server_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MasterSteamPort = n
		}
	}
	if v := masterINI.get("STEAM", "authentication_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MasterAuthPort = n
		}
	}
	cavesINI := readINI(filepath.Join(dir, "Caves", "server.ini"))
	if v := cavesINI.get("NETWORK", "server_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CavesPort = n
			c.Caves = true
		}
	}
	if v := cavesINI.get("STEAM", "master_server_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CavesSteamPort = n
		}
	}
	if v := cavesINI.get("STEAM", "authentication_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CavesAuthPort = n
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Caves")); err == nil {
		c.Caves = c.Caves || cavesINI.get("SHARD", "is_master") == "false"
	}
	// unit names: the legacy cluster (built by hand earlier) uses dst-server /
	// dst-server-caves; worlds created by the panel use dst-<slug>-<shard>.
	legacy := filepath.Clean(dir) == filepath.Clean(a.cfg.ClusterDir)
	if legacy && len(a.cfg.Shards) > 0 {
		for _, sh := range a.cfg.Shards {
			switch sh.Name {
			case "Master":
				c.MasterUnit = sh.Service
				c.MasterPort = sh.Port
			case "Caves":
				c.CavesUnit = sh.Service
				c.CavesPort = sh.Port
				c.Caves = true
			}
		}
	}
	if c.MasterUnit == "" {
		c.MasterUnit = unitName(name, "Master")
	}
	if c.Caves && c.CavesUnit == "" {
		c.CavesUnit = unitName(name, "Caves")
	}
	return c
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func clusterSlug(name string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "world"
	}
	return s
}

func unitName(cluster, shard string) string {
	return "dst-" + clusterSlug(cluster) + "-" + strings.ToLower(shard)
}

var worldNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{3,20}$`)

func validWorldName(name string) error {
	if !worldNameRe.MatchString(name) {
		return fmt.Errorf("世界名称只能包含字母、数字、下划线和短横线，长度 3–20")
	}
	return nil
}

// ---- INI helpers ----

type iniFile map[string]map[string]string

func (f iniFile) get(section, key string) string {
	if s, ok := f[section]; ok {
		if v, ok := s[key]; ok {
			return v
		}
	}
	return ""
}

func readINI(path string) iniFile {
	out := iniFile{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToUpper(strings.Trim(line, "[]"))
			out[section] = map[string]string{}
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.TrimSpace(line[i+1:])
			if out[section] == nil {
				out[section] = map[string]string{}
			}
			out[section][k] = v
		}
	}
	return out
}

// ---- cluster lookup ----

func (a *Agent) activeCluster() (clusterDef, error) {
	rf, err := a.loadRegistry()
	if err != nil {
		return clusterDef{}, err
	}
	for _, c := range rf.Clusters {
		if c.Name == rf.Active {
			return c, nil
		}
	}
	if len(rf.Clusters) > 0 {
		return rf.Clusters[0], nil
	}
	return a.clusterFromDir(a.cfg.ClusterDir), nil
}

func (a *Agent) clusterByName(name string) (clusterDef, error) {
	rf, err := a.loadRegistry()
	if err != nil {
		return clusterDef{}, err
	}
	for _, c := range rf.Clusters {
		if c.Name == name {
			return c, nil
		}
	}
	return clusterDef{}, fmt.Errorf("没有叫 %s 的世界", name)
}

// clusterDir resolves the absolute dir of a cluster (name or "" for active).
func (a *Agent) resolveCluster(name string) (clusterDef, error) {
	if strings.TrimSpace(name) == "" {
		return a.activeCluster()
	}
	return a.clusterByName(name)
}

func (a *Agent) shardDir(c clusterDef, shard string) string {
	return filepath.Join(c.Dir, shard)
}

// ---- listing / status ----

func (a *Agent) clustersList(ctx context.Context) (*proto.ClustersReply, error) {
	rf, err := a.loadRegistry()
	if err != nil {
		return nil, err
	}
	out := &proto.ClustersReply{Active: rf.Active}
	for _, c := range rf.Clusters {
		out.Clusters = append(out.Clusters, a.clusterBrief(ctx, c, rf.Active == c.Name))
	}
	return out, nil
}

func (a *Agent) clusterBrief(ctx context.Context, c clusterDef, active bool) proto.ClusterBrief {
	b := proto.ClusterBrief{
		Name:         c.Name,
		Title:        c.Title,
		Dir:          c.Dir,
		Active:       active,
		GameMode:     c.GameMode,
		GameModeName: a.playstyleName(c.GameMode),
		Description:  c.Description,
		Password:     c.Password,
		MaxPlayers:   c.MaxPlayers,
		PvP:          c.PvP,
		Caves:        c.Caves,
		MasterPort:   c.MasterPort,
		CavesPort:    c.CavesPort,
		SteamPort:    c.MasterSteamPort,
		AuthPort:     c.MasterAuthPort,
		CreatedAt:    c.CreatedAt,
	}
	running := false
	for _, sh := range c.shards(a.cfg) {
		active, restarts := a.run.serviceState(ctx, sh.Service)
		b.Shards = append(b.Shards, proto.ShardStatus{
			Name: sh.Name, Service: sh.Service, Active: active, Port: sh.Port, Restarts: restarts,
		})
		running = running || active
	}
	b.Running = running
	return b
}

func (a *Agent) activateCluster(name string) error {
	if _, err := a.clusterByName(name); err != nil {
		return err
	}
	rf, err := a.loadRegistry()
	if err != nil {
		return err
	}
	rf.Active = name
	return a.saveRegistry(rf)
}

// ---- service control ----

// stopOtherClusters stops every world except keep (2C4G can only run one world).
func (a *Agent) stopOtherClusters(ctx context.Context, job *proto.Job, keep string) error {
	rf, err := a.loadRegistry()
	if err != nil {
		return err
	}
	for _, c := range rf.Clusters {
		if c.Name == keep {
			continue
		}
		for _, sh := range c.shards(a.cfg) {
			active, _ := a.run.serviceState(ctx, sh.Service)
			if !active {
				continue
			}
			a.jobLog(job, fmt.Sprintf("停止其它世界 %s/%s（内存只够跑一个世界）", c.Name, sh.Name))
			if err := a.run.stopService(ctx, sh.Service); err != nil {
				a.jobLog(job, "  停止失败: "+err.Error())
			}
		}
	}
	return nil
}

func (a *Agent) startCluster(ctx context.Context, job *proto.Job, c clusterDef) error {
	if err := a.stopOtherClusters(ctx, job, c.Name); err != nil {
		return err
	}
	for _, sh := range c.shards(a.cfg) {
		since := time.Now().Add(-2 * time.Second)
		a.jobLog(job, "启动 "+sh.Service+" …")
		if err := a.run.startService(ctx, sh.Service); err != nil {
			return fmt.Errorf("启动 %s 失败: %w", sh.Service, err)
		}
		if err := a.run.waitStarted(ctx, sh.Service, since, 8*time.Minute); err != nil {
			a.jobLog(job, "  警告: "+err.Error())
		} else {
			a.jobLog(job, "  "+sh.Service+" 已就绪（端口 "+strconv.Itoa(sh.Port)+"）")
		}
	}
	return a.activateCluster(c.Name)
}

func (a *Agent) stopCluster(ctx context.Context, job *proto.Job, c clusterDef) error {
	for _, sh := range c.shards(a.cfg) {
		active, _ := a.run.serviceState(ctx, sh.Service)
		if !active {
			continue
		}
		a.jobLog(job, "停止 "+sh.Service+" …")
		if err := a.run.stopService(ctx, sh.Service); err != nil {
			return err
		}
	}
	return nil
}

// ---- creation ----

// portSlot is the full UDP port set one world needs. All of these must fall
// inside the cloud security group ranges (README §安全组):
//
//	game  10998–11018 / steam-query 27015–27020 / auth 8766–8770
type portSlot struct {
	MasterPort, CavesPort           int
	MasterSteam, CavesSteam         int
	MasterAuth, CavesAuth           int
	ShardPort                       int
}

func (a *Agent) nextPortSlot(rf *registryFile) portSlot {
	used := map[int]bool{}
	for _, c := range rf.Clusters {
		used[c.MasterPort], used[c.CavesPort], used[c.ShardPort] = true, true, true
		used[c.MasterSteamPort], used[c.CavesSteamPort] = true, true
		used[c.MasterAuthPort], used[c.CavesAuthPort] = true, true
	}
	pickPair := func(from, to int) (int, int) {
		for p := from; p+1 <= to; p += 2 {
			if !used[p] && !used[p+1] {
				used[p], used[p+1] = true, true
				return p, p + 1
			}
		}
		return from, from + 1
	}
	// 游戏端口从 11000 起（10998/10999 属于最早那台世界），Steam 查询与认证
	// 端口从 27015 / 8766 起；三者都必须是安全组里已经放行的范围。
	game, game2 := pickPair(11000, 11018)
	steam, steam2 := pickPair(27015, 27020)
	auth, auth2 := pickPair(8766, 8770)
	shard := 10889
	for used[shard] {
		shard++
	}
	return portSlot{
		MasterPort: game, CavesPort: game2,
		MasterSteam: steam, CavesSteam: steam2,
		MasterAuth: auth, CavesAuth: auth2,
		ShardPort: shard,
	}
}

func (a *Agent) createWorld(ctx context.Context, job *proto.Job, p proto.CreateWorldParams) error {
	p.Name = strings.TrimSpace(p.Name)
	if err := validWorldName(p.Name); err != nil {
		return err
	}
	switch p.GameMode {
	case "", "survival":
		p.GameMode = "survival"
	case "relaxed", "endless", "wilderness", "lightsout":
	default:
		return fmt.Errorf("未知的游戏模式 %q", p.GameMode)
	}
	if p.MaxPlayers < 1 {
		p.MaxPlayers = 6
	}
	if p.MaxPlayers > 16 {
		p.MaxPlayers = 16
	}
	// 预设校验：地上分片必须是地上预设、洞穴分片必须是洞穴预设；空值补默认。
	// 漏掉这一步就会创建出"两个地上世界"的世界（洞穴进不去）。
	masterPreset, err := normalizePreset("Master", p.MasterPreset)
	if err != nil {
		return err
	}
	p.MasterPreset = masterPreset
	if p.Caves {
		cavesPreset, cerr := normalizePreset("Caves", p.CavesPreset)
		if cerr != nil {
			return cerr
		}
		p.CavesPreset = cavesPreset
	}
	a.jobLog(job, fmt.Sprintf("世界预设：地上 %s%s", p.MasterPreset,
		map[bool]string{true: "，洞穴 " + p.CavesPreset, false: ""}[p.Caves]))

	rf, err := a.loadRegistry()
	if err != nil {
		return err
	}
	for _, c := range rf.Clusters {
		if strings.EqualFold(c.Name, p.Name) {
			return fmt.Errorf("已经有叫 %s 的世界了，换一个名字", p.Name)
		}
	}
	parent := filepath.Dir(a.cfg.ClusterDir)
	dir := filepath.Join(parent, p.Name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("目录 %s 已存在", dir)
	}

	slot := a.nextPortSlot(rf)
	c := clusterDef{
		Name:            p.Name,
		Title:           p.Name,
		Dir:             dir,
		GameMode:        p.GameMode,
		Description:     p.Description,
		Password:        p.Password,
		MaxPlayers:      p.MaxPlayers,
		PvP:             p.PvP,
		Caves:           p.Caves,
		MasterPort:      slot.MasterPort,
		CavesPort:       slot.CavesPort,
		ShardPort:       slot.ShardPort,
		MasterSteamPort: slot.MasterSteam,
		CavesSteamPort:  slot.CavesSteam,
		MasterAuthPort:  slot.MasterAuth,
		CavesAuthPort:   slot.CavesAuth,
		MasterUnit:      unitName(p.Name, "Master"),
		CavesUnit:       unitName(p.Name, "Caves"),
		CreatedAt:       time.Now().Unix(),
	}
	a.jobLog(job, fmt.Sprintf("创建世界 %s：地上端口 %d%s，Steam 查询/认证 %d/%d", c.Name, c.MasterPort,
		map[bool]string{true: fmt.Sprintf("，洞穴端口 %d", c.CavesPort), false: "（不开洞穴）"}[c.Caves],
		c.MasterSteamPort, c.MasterAuthPort))

	// 1) 配置目录
	for _, sub := range []string{"", "Master", "Caves", "Master/save", "Caves/save"} {
		if sub == "Caves" || strings.HasPrefix(sub, "Caves/") {
			if !c.Caves {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := a.writeClusterConfigs(c, p, job); err != nil {
		return err
	}
	a.jobLog(job, "已写入 cluster.ini / server.ini / worldgenoverride.lua")

	// 2) token + 管理员名单（同一个 Klei 账号，直接复制）
	if err := a.copyClusterExtras(c, rf); err != nil {
		return err
	}
	a.jobLog(job, "已复制 cluster_token.txt / adminlist.txt")

	// 3) systemd 单元
	if err := a.writeUnits(c); err != nil {
		return err
	}
	if _, err := a.run.run(ctx, 30*time.Second, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	a.jobLog(job, "已安装 systemd 单元 "+c.MasterUnit+map[bool]string{true: " / " + c.CavesUnit, false: ""}[c.Caves])

	// 4) 模组：同步到新世界的 ugc 目录 + 写 modoverrides
	if p.CopyMods {
		if err := a.syncModsToCluster(ctx, job, c); err != nil {
			a.jobLog(job, "警告: 同步模组失败: "+err.Error())
		}
	}

	// 5) 归属与注册
	_, _ = a.run.run(ctx, 60*time.Second, "chown", "-R", a.cfg.GameUser+":"+a.cfg.GameUser, dir)
	rf.Clusters = append(rf.Clusters, c)
	if p.Start {
		rf.Active = c.Name
	}
	if err := a.saveRegistry(rf); err != nil {
		return err
	}
	if p.Start {
		a.jobLog(job, "启动新世界 …")
		return a.startCluster(ctx, job, c)
	}
	a.jobLog(job, "世界已创建（未启动）。在“世界”页点“启动”即可开服。")
	return nil
}

func (a *Agent) writeClusterConfigs(c clusterDef, p proto.CreateWorldParams, job *proto.Job) error {
	desc := strings.TrimSpace(p.Description)
	if desc == "" {
		desc = "Campfire dedicated server"
	}
	name := p.Name
	var b strings.Builder
	b.WriteString("[GAMEPLAY]\n")
	b.WriteString("game_mode = " + c.GameMode + "\n")
	b.WriteString(fmt.Sprintf("max_players = %d\n", c.MaxPlayers))
	b.WriteString(fmt.Sprintf("pvp = %t\n", c.PvP))
	b.WriteString("pause_when_empty = true\n\n")
	b.WriteString("[NETWORK]\n")
	b.WriteString("cluster_name = " + name + "\n")
	b.WriteString("cluster_description = " + desc + "\n")
	b.WriteString("cluster_password = " + p.Password + "\n")
	b.WriteString("cluster_intention = cooperative\n")
	b.WriteString("tick_rate = 15\n\n")
	b.WriteString("[MISC]\n")
	b.WriteString("console_enabled = true\n")
	b.WriteString("max_snapshots = 6\n")
	b.WriteString("autosaver_enabled = true\n\n")
	b.WriteString("[SHARD]\n")
	b.WriteString(fmt.Sprintf("shard_enabled = %t\n", c.Caves))
	b.WriteString("bind_ip = 127.0.0.1\n")
	b.WriteString("master_ip = 127.0.0.1\n")
	b.WriteString(fmt.Sprintf("master_port = %d\n", c.ShardPort))
	b.WriteString("cluster_key = campfire-" + clusterSlug(c.Name) + "\n")
	if err := writeFileAtomic(filepath.Join(c.Dir, "cluster.ini"), []byte(b.String()), 0o644); err != nil {
		return err
	}

	writeServer := func(shard, name string, id int, port, masterSrvPort, authPort int, isMaster bool) error {
		var sb strings.Builder
		sb.WriteString("[NETWORK]\n")
		sb.WriteString(fmt.Sprintf("server_port = %d\n\n", port))
		sb.WriteString("[SHARD]\n")
		sb.WriteString(fmt.Sprintf("is_master = %t\n", isMaster))
		sb.WriteString("name = " + name + "\n")
		sb.WriteString(fmt.Sprintf("id = %d\n\n", id))
		sb.WriteString("[STEAM]\n")
		sb.WriteString(fmt.Sprintf("master_server_port = %d\n", masterSrvPort))
		sb.WriteString(fmt.Sprintf("authentication_port = %d\n\n", authPort))
		sb.WriteString("[ACCOUNT]\n")
		sb.WriteString("encode_user_path = true\n")
		return writeFileAtomic(filepath.Join(c.Dir, shard, "server.ini"), []byte(sb.String()), 0o644)
	}
	// Steam 查询/认证端口优先用分配好的槽位；老世界没有记录时按旧公式回退。
	mSteam, mAuth := c.MasterSteamPort, c.MasterAuthPort
	if mSteam == 0 {
		mSteam = c.MasterPort + 16017
	}
	if mAuth == 0 {
		mAuth = c.MasterPort - 2233
	}
	if err := writeServer("Master", "Master", 1, c.MasterPort, mSteam, mAuth, true); err != nil {
		return err
	}
	if c.Caves {
		cSteam, cAuth := c.CavesSteamPort, c.CavesAuthPort
		if cSteam == 0 {
			cSteam = c.CavesPort + 16019
		}
		if cAuth == 0 {
			cAuth = c.CavesPort - 2232
		}
		if err := writeServer("Caves", "Caves", 2, c.CavesPort, cSteam, cAuth, false); err != nil {
			return err
		}
	}
	// worldgenoverride.lua —— 预设 + 世界规则（不要写 leveldataoverride.lua）
	// 每个分片只写它自己支持的设置项：地上页的野火/季节不会被塞进洞穴分片，
	// 洞穴页的地震也不会被塞进地上分片。
	mdropped, err := a.writeWorldgenOverride(c, "Master", p.MasterPreset, p.MasterOverrides)
	if err != nil {
		return err
	}
	if len(mdropped) > 0 {
		a.jobLog(job, fmt.Sprintf("地上世界忽略 %d 项不属于该分片的设置：%s", len(mdropped), strings.Join(mdropped, "、")))
	}
	if c.Caves {
		cdropped, err := a.writeWorldgenOverride(c, "Caves", p.CavesPreset, p.CavesOverrides)
		if err != nil {
			return err
		}
		if len(cdropped) > 0 {
			a.jobLog(job, fmt.Sprintf("洞穴忽略 %d 项不属于该分片的设置：%s", len(cdropped), strings.Join(cdropped, "、")))
		}
	}
	return nil
}

// copyClusterExtras copies the Klei token / admin list from any existing world.
func (a *Agent) copyClusterExtras(c clusterDef, rf *registryFile) error {
	var srcDir string
	var tokenFound bool
	for _, other := range rf.Clusters {
		if _, err := os.Stat(filepath.Join(other.Dir, "cluster_token.txt")); err == nil {
			srcDir = other.Dir
			tokenFound = true
			break
		}
	}
	if !tokenFound {
		if _, err := os.Stat(filepath.Join(a.cfg.ClusterDir, "cluster_token.txt")); err == nil {
			srcDir = a.cfg.ClusterDir
			tokenFound = true
		}
	}
	if !tokenFound {
		return fmt.Errorf("找不到 cluster_token.txt：请先在任意世界里放好 Klei 令牌")
	}
	token, err := os.ReadFile(filepath.Join(srcDir, "cluster_token.txt"))
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(c.Dir, "cluster_token.txt"), token, 0o600); err != nil {
		return err
	}
	for _, extra := range []string{"adminlist.txt", "whitelist.txt", "blocklist.txt"} {
		data, err := os.ReadFile(filepath.Join(srcDir, extra))
		if err != nil {
			continue
		}
		if err := writeFileAtomic(filepath.Join(c.Dir, extra), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

const unitTemplate = `[Unit]
Description=DST %s (%s) — Campfire
After=network.target

[Service]
Type=simple
User=%s
WorkingDirectory=%s/bin64
Environment=HOME=%s
ExecStart=%s %s %s %s
Restart=on-failure
RestartSec=10
LimitNOFILE=8192

[Install]
WantedBy=multi-user.target
`

func (a *Agent) writeUnits(c clusterDef) error {
	shards := c.shards(a.cfg)
	for _, sh := range shards {
		body := fmt.Sprintf(unitTemplate, c.Name, sh.Name, a.cfg.GameUser,
			a.cfg.InstallDir, a.cfg.Home, shardRunnerPath, c.Name, sh.Name, a.cfg.InstallDir)
		path := filepath.Join("/etc/systemd/system", sh.Service+".service")
		if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) removeWorld(ctx context.Context, job *proto.Job, name string) error {
	c, err := a.clusterByName(name)
	if err != nil {
		return err
	}
	rf, err := a.loadRegistry()
	if err != nil {
		return err
	}
	if len(rf.Clusters) <= 1 {
		return fmt.Errorf("至少要保留一个世界")
	}
	if err := a.stopCluster(ctx, job, c); err != nil {
		return err
	}
	// 备份存档后删除目录
	backup := filepath.Join(a.cfg.Home, "backups",
		fmt.Sprintf("%s-removed-%s.tar.gz", c.Name, time.Now().Format("20060102-150405")))
	if _, err := a.run.run(ctx, 3*time.Minute, "mkdir", "-p", filepath.Dir(backup)); err != nil {
		return err
	}
	if _, err := a.run.run(ctx, 10*time.Minute, "tar", "czf", backup, "-C", filepath.Dir(c.Dir), filepath.Base(c.Dir)); err == nil {
		a.jobLog(job, "存档已备份到 "+backup)
	}
	for _, sh := range c.shards(a.cfg) {
		_ = os.Remove(filepath.Join("/etc/systemd/system", sh.Service+".service"))
		if _, err := a.run.run(ctx, 30*time.Second, "systemctl", "disable", sh.Service); err != nil {
			_ = err
		}
	}
	if _, err := a.run.run(ctx, 30*time.Second, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := os.RemoveAll(c.Dir); err != nil {
		return err
	}
	var kept []clusterDef
	for _, other := range rf.Clusters {
		if other.Name != c.Name {
			kept = append(kept, other)
		}
	}
	rf.Clusters = kept
	if rf.Active == c.Name {
		rf.Active = kept[0].Name
	}
	a.jobLog(job, "已删除世界 "+c.Name+"（系统单元与目录都已清理）")
	return a.saveRegistry(rf)
}

// playstyleName maps a game_mode to its Chinese playstyle name.
func (a *Agent) playstyleName(mode string) string {
	cat := worldgenCatalogData()
	for _, p := range cat.Playstyles {
		if p.ID == mode {
			return p.Name
		}
	}
	return mode
}

// seedFromShardIndex digs the world seed out of a shardindex file (best effort).
func seedFromShardIndex(src string) string {
	i := strings.Index(src, "seed")
	if i < 0 {
		return ""
	}
	tail := src[i:]
	j := strings.Index(tail, "=")
	if j < 0 {
		return ""
	}
	tail = strings.TrimSpace(tail[j+1:])
	end := 0
	for end < len(tail) && (tail[end] >= '0' && tail[end] <= '9') {
		end++
	}
	return tail[:end]
}