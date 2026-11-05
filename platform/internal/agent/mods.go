package agent

// 模组管理：清单（mods.list，全局）、ugc 内容同步（每世界每分片一份）、
// modoverrides.lua（每分片一份）、单个/全部更新、详情与 Steam 元数据。

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"campfire/internal/luaconf"
	"campfire/internal/proto"
)

// ---- mods.list ----

type listEntry struct {
	Kind    string // "mod" or "collection"
	ID      string
	Enabled bool
}

func (a *Agent) readModsList() ([]listEntry, error) {
	data, err := os.ReadFile(a.cfg.ModsList)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []listEntry
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e := listEntry{Enabled: true}
		if i := strings.LastIndex(line, ":"); i >= 0 && line[i+1:] == "disabled" {
			e.Enabled = false
			line = line[:i]
		}
		if i := strings.Index(line, ":"); i > 0 {
			e.Kind = line[:i]
			e.ID = strings.TrimSpace(line[i+1:])
		}
		if e.ID == "" || !isDigits(e.ID) {
			continue
		}
		if e.Kind != "collection" {
			e.Kind = "mod"
		}
		out = append(out, e)
	}
	return out, nil
}

func (a *Agent) saveModsList(entries []listEntry) error {
	var b strings.Builder
	b.WriteString("# Campfire 模组清单 —— 面板会读写这个文件\n")
	b.WriteString("# 格式：mod:<创意工坊ID> 或 collection:<合集ID>，行尾 :disabled 表示停用\n")
	for _, e := range entries {
		line := e.Kind + ":" + e.ID
		if !e.Enabled {
			line += ":disabled"
		}
		b.WriteString(line + "\n")
	}
	if err := writeFileAtomic(a.cfg.ModsList, []byte(b.String()), 0o644); err != nil {
		return err
	}
	_, _ = a.run.run(context.Background(), 10*time.Second, "chown", a.cfg.GameUser+":"+a.cfg.GameUser, a.cfg.ModsList)
	return nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasEntry(entries []listEntry, id string) bool {
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

// enabledModIDs returns the workshop mod ids that should be installed on a
// cluster (collections are expanded through the Steam API, cached on disk).
func (a *Agent) enabledModIDs(ctx context.Context) ([]string, error) {
	entries, err := a.readModsList()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		if e.Kind == "collection" {
			items, err := getCollectionDetails(ctx, e.ID)
			if err != nil {
				continue
			}
			for _, it := range items {
				add(it)
			}
			continue
		}
		add(e.ID)
	}
	return ids, nil
}

// ---- paths ----

func (a *Agent) overridesPath(c clusterDef, shard string) string {
	return filepath.Join(c.Dir, shard, "modoverrides.lua")
}

// ugcContentDir is the directory the game actually loads mods from.
func (a *Agent) ugcContentDir(c clusterDef, shard string) string {
	return filepath.Join(a.cfg.InstallDir, "ugc_mods", c.Name, shard, "content", "322330")
}

func (a *Agent) ugcDir(c clusterDef, shard, id string) string {
	return filepath.Join(a.ugcContentDir(c, shard), id)
}

// cachedModDir is where steamcmd puts downloaded workshop content.
func (a *Agent) cachedModDir(id string) string {
	return filepath.Join(a.cfg.InstallDir, "steamapps", "workshop", "content", "322330", id)
}

// ---- modoverrides.lua ----

func (a *Agent) readOverrides(c clusterDef, shard string) (*luaconf.Value, error) {
	data, err := os.ReadFile(a.overridesPath(c, shard))
	if err != nil {
		if os.IsNotExist(err) {
			return &luaconf.Value{Kind: luaconf.KTable, Map: map[string]*luaconf.Value{}}, nil
		}
		return nil, err
	}
	chunk, err := luaconf.Parse(string(data))
	if err != nil {
		return nil, err
	}
	if chunk.Return == nil || chunk.Return.Kind != luaconf.KTable {
		return &luaconf.Value{Kind: luaconf.KTable, Map: map[string]*luaconf.Value{}}, nil
	}
	if chunk.Return.Map == nil {
		chunk.Return.Map = map[string]*luaconf.Value{}
	}
	return chunk.Return, nil
}

func (a *Agent) writeOverrides(c clusterDef, shard string, tbl *luaconf.Value) error {
	path := a.overridesPath(c, shard)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := "-- Campfire: 由面板生成，可手工修改，下次保存会被覆盖\nreturn " + luaconf.Render(tbl) + "\n"
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return err
	}
	_, _ = a.run.run(context.Background(), 15*time.Second, "chown", a.cfg.GameUser+":"+a.cfg.GameUser, path)
	return nil
}

type overrideEntry struct {
	Enabled bool
	Config  map[string]*luaconf.Value
}

func parseOverrideEntry(v *luaconf.Value) overrideEntry {
	out := overrideEntry{Config: map[string]*luaconf.Value{}}
	if v == nil || v.Kind != luaconf.KTable {
		return out
	}
	if en, ok := v.Map["enabled"]; ok && en.Kind == luaconf.KBool {
		out.Enabled = en.Bool
	} else {
		out.Enabled = true
	}
	if cfg, ok := v.Map["configuration_options"]; ok && cfg.Kind == luaconf.KTable {
		for k, val := range cfg.Map {
			out.Config[k] = val
		}
	}
	return out
}

func renderOverrideEntry(e overrideEntry) *luaconf.Value {
	tbl := &luaconf.Value{Kind: luaconf.KTable, Map: map[string]*luaconf.Value{
		"enabled": {Kind: luaconf.KBool, Bool: e.Enabled},
	}}
	if len(e.Config) > 0 {
		cfg := &luaconf.Value{Kind: luaconf.KTable, Map: map[string]*luaconf.Value{}}
		for _, k := range sortedKeys(e.Config) {
			cfg.Map[k] = e.Config[k]
		}
		tbl.Map["configuration_options"] = cfg
	}
	return tbl
}

func coerceScalar(s string) *luaconf.Value {
	switch s {
	case "true":
		return &luaconf.Value{Kind: luaconf.KBool, Bool: true}
	case "false":
		return &luaconf.Value{Kind: luaconf.KBool, Bool: false}
	case "nil":
		return &luaconf.Value{Kind: luaconf.KNil}
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, " \t") {
		return &luaconf.Value{Kind: luaconf.KNumber, Num: n}
	}
	return &luaconf.Value{Kind: luaconf.KString, Str: s}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// overridesForShard reads the per-mod config from an existing overrides file.
func (a *Agent) overridesForShard(c clusterDef, shard string) map[string]overrideEntry {
	out := map[string]overrideEntry{}
	tbl, err := a.readOverrides(c, shard)
	if err != nil || tbl == nil {
		return out
	}
	for k, v := range tbl.Map {
		if !strings.HasPrefix(k, "workshop-") {
			continue
		}
		id := strings.TrimPrefix(k, "workshop-")
		out[id] = parseOverrideEntry(v)
	}
	return out
}

// writeModoverrides regenerates modoverrides.lua for every shard of a cluster,
// preserving configuration_options that were set earlier.
func (a *Agent) writeModoverrides(c clusterDef, ids []string) error {
	for _, sh := range c.shards(a.cfg) {
		existing := a.overridesForShard(c, sh.Name)
		tbl := &luaconf.Value{Kind: luaconf.KTable, Map: map[string]*luaconf.Value{}}
		for _, id := range ids {
			e := existing[id]
			e.Enabled = true
			tbl.Map["workshop-"+id] = renderOverrideEntry(e)
		}
		if err := a.writeOverrides(c, sh.Name, tbl); err != nil {
			return err
		}
	}
	return nil
}

// ---- modinfo.lua ----

type modinfo struct {
	Name       string
	Version    string
	Author     string
	Desc       string
	ClientOnly bool
	AllClients bool
	Options    []proto.ModOption
}

func (a *Agent) findModinfo(c clusterDef, id string) (*modinfo, string) {
	for _, sh := range c.shards(a.cfg) {
		dir := a.ugcDir(c, sh.Name, id)
		if mi := parseModinfo(filepath.Join(dir, "modinfo.lua")); mi != nil {
			return mi, dir
		}
		if cached := a.cachedModDir(id); cached != "" {
			if mi := parseModinfo(filepath.Join(cached, "modinfo.lua")); mi != nil {
				return mi, cached
			}
		}
	}
	return nil, ""
}

var (
	verRe     = regexp.MustCompile(`(?m)^\s*version\s*=\s*"([^"]*)"`)
	versionFn = regexp.MustCompile(`(?s)version\s*=\s*function\s*\(\s*\)\s*return\s*"([^"]*)"`)
)

func parseModinfo(path string) *modinfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	src := string(data)
	chunk, err := luaconf.Parse(src)
	if err != nil {
		return nil
	}
	fields := chunk.Fields
	mi := &modinfo{}
	str := func(k string) string {
		if v, ok := fields[k]; ok && v.Kind == luaconf.KString {
			return v.Str
		}
		return ""
	}
	boolean := func(k string) bool {
		if v, ok := fields[k]; ok && v.Kind == luaconf.KBool {
			return v.Bool
		}
		return false
	}
	mi.Name = str("name")
	mi.Version = str("version")
	if mi.Version == "" {
		if m := versionFn.FindStringSubmatch(src); m != nil {
			mi.Version = m[1]
		} else if m := verRe.FindStringSubmatch(src); m != nil {
			mi.Version = m[1]
		}
	}
	mi.Author = str("author")
	mi.Desc = str("description")
	mi.ClientOnly = boolean("client_only_mod")
	mi.AllClients = boolean("all_clients_require_mod")
	if o, ok := fields["configuration_options"]; ok && o.Kind == luaconf.KTable {
		for _, opt := range o.List {
			if opt.Kind != luaconf.KTable {
				continue
			}
			name := ""
			if v, ok := opt.Map["name"]; ok && v.Kind == luaconf.KString {
				name = v.Str
			}
			if name == "" {
				continue
			}
			label := name
			if v, ok := opt.Map["label"]; ok && v.Kind == luaconf.KString {
				label = v.Str
			}
			hover := ""
			if v, ok := opt.Map["hover"]; ok && v.Kind == luaconf.KString {
				hover = v.Str
			}
			def := "nil"
			if v, ok := opt.Map["default"]; ok {
				def = choiceText(v, "nil")
			}
			mo := proto.ModOption{Name: name, Label: label, Hover: hover, Default: def}
			if opts, ok := opt.Map["options"]; ok && opts.Kind == luaconf.KTable {
				for _, ch := range opts.List {
					label := ""
					value := ""
					if ch.Kind == luaconf.KTable {
						if v, ok := ch.Map["description"]; ok && v.Kind == luaconf.KString {
							label = v.Str
						}
						if v, ok := ch.Map["data"]; ok {
							value = choiceText(v, "")
						}
					}
					if value != "" {
						mo.Choices = append(mo.Choices, proto.ModChoice{Label: label, Value: value})
					}
				}
			}
			mi.Options = append(mi.Options, mo)
		}
	}
	return mi
}

func choiceText(v *luaconf.Value, unset string) string {
	switch v.Kind {
	case luaconf.KString:
		return v.Str
	case luaconf.KBool:
		return boolText(v.Bool)
	case luaconf.KNumber:
		if v.Num == float64(int64(v.Num)) {
			return strconv.FormatInt(int64(v.Num), 10)
		}
		return strconv.FormatFloat(v.Num, 'f', -1, 64)
	}
	return unset
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ---- listing ----

func (a *Agent) modsList(ctx context.Context, c clusterDef) (*proto.ModsList, error) {
	entries, err := a.readModsList()
	if err != nil {
		return nil, err
	}
	out := &proto.ModsList{}
	var enabledIDs []string
	for _, e := range entries {
		ids := []string{e.ID}
		if e.Kind == "collection" {
			children, err := getCollectionDetails(ctx, e.ID)
			if err == nil {
				ids = children
			}
			out.Collections = append(out.Collections, e.ID)
		}
		_ = ids
		if e.Enabled {
			if e.Kind == "collection" {
				children, err := getCollectionDetails(ctx, e.ID)
				if err == nil {
					enabledIDs = append(enabledIDs, children...)
				}
			} else {
				enabledIDs = append(enabledIDs, e.ID)
			}
		}
	}
	// configuration options as they are written on the Master shard
	cfg := a.overridesForShard(c, "Master")
	seen := map[string]bool{}
	for _, id := range enabledIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		mi := proto.ModInfo{ID: id, Enabled: true}
		if info, dir := a.findModinfo(c, id); info != nil {
			mi.Installed = true
			mi.Name = info.Name
			mi.Version = info.Version
			mi.Author = info.Author
			mi.Description = info.Desc
			mi.ClientOnly = info.ClientOnly
			mi.AllClients = info.AllClients
			mi.Options = info.Options
			mi.SizeBytes = dirSize(dir)
			if st, err := os.Stat(dir); err == nil {
				mi.UpdatedAt = st.ModTime().Unix()
			}
		}
		mi.Current = map[string]string{}
		if e, ok := cfg[id]; ok {
			for k, v := range e.Config {
				mi.Current[k] = choiceText(v, "")
			}
		}
		out.Mods = append(out.Mods, mi)
	}
	a.enrichWorkshopTitles(ctx, out)
	return out, nil
}


// enrichWorkshopTitles fills workshop titles / subscription counts / update
// markers from the cached Steam metadata (never blocks on the network for
// entries already in the cache).
func (a *Agent) enrichWorkshopTitles(ctx context.Context, list *proto.ModsList) {
	var need []string
	for i := range list.Mods {
		m := &list.Mods[i]
		if m.WorkshopTitle == "" || m.PreviewURL == "" {
			need = append(need, m.ID)
		}
	}
	meta := map[string]workshopPreview{}
	if len(need) > 0 {
		if details, err := a.getPublishedFileDetailsCached(ctx, need); err == nil {
			for _, d := range details {
				meta[d.ID] = d
			}
		}
	}
	for i := range list.Mods {
		m := &list.Mods[i]
		d, ok := meta[m.ID]
		if !ok {
			continue
		}
		m.WorkshopTitle = d.Title
		m.PreviewURL = d.PreviewURL
		m.Subscriptions = d.Subscriptions
		if m.Name == "" || strings.HasPrefix(m.Name, "workshop-") || strings.HasPrefix(m.Name, "__") {
			m.Name = d.Title
		}
		if junkDescription(m.Description) || len(m.Description) < len(d.Description)/2 {
			m.Description = clipText(d.Description, 600)
		}
		if d.UpdatedAt > m.UpdatedAt+60 {
			m.NeedsUpdate = true
		}
		if m.NeedsUpdate {
			list.NeedsUpdate = true
		}
	}
}

func junkDescription(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 12 {
		return true
	}
	low := strings.ToLower(s)
	for _, bad := range []string{"string_format", "function(", "local ", "return ", "table."} {
		if strings.Contains(low, bad) {
			return true
		}
	}
	return false
}

func clipText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexAny(cut, "。！？\n"); i > max/2 {
		return cut[:i+len("。")]
	}
	return cut + "…"
}
// ---- mutations ----

func (a *Agent) setOverridesFor(c clusterDef, id string, mutate func(*overrideEntry)) error {
	for _, sh := range c.shards(a.cfg) {
		tbl, err := a.readOverrides(c, sh.Name)
		if err != nil {
			return err
		}
		key := "workshop-" + id
		e := parseOverrideEntry(tbl.Map[key])
		mutate(&e)
		tbl.Map[key] = renderOverrideEntry(e)
		if err := a.writeOverrides(c, sh.Name, tbl); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) modsSetEnabled(ctx context.Context, clusterName, id string, enabled bool) (*proto.Job, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	entries, err := a.readModsList()
	if err != nil {
		return nil, err
	}
	if enabled && !hasEntry(entries, id) {
		entries = append(entries, listEntry{Kind: "mod", ID: id, Enabled: true})
	} else if !enabled {
		var kept []listEntry
		for _, e := range entries {
			if e.ID != id {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	if err := a.saveModsList(entries); err != nil {
		return nil, err
	}
	return a.startJob("mods.enable", func(job *proto.Job) error {
		if enabled {
			a.jobLog(job, "启用模组 workshop-"+id)
		} else {
			a.jobLog(job, "停用模组 workshop-"+id)
		}
		if err := a.applyMods(ctx, job, c, "重启服务器 …"); err != nil {
			return err
		}
		return nil
	})
}

func (a *Agent) modsSetConfig(ctx context.Context, clusterName, id string, options map[string]string) (*proto.Job, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	if err := a.setOverridesFor(c, id, func(e *overrideEntry) {
		if e.Config == nil {
			e.Config = map[string]*luaconf.Value{}
		}
		for k, v := range options {
			e.Config[k] = coerceScalar(v)
		}
	}); err != nil {
		return nil, err
	}
	return a.startJob("mods.config", func(job *proto.Job) error {
		a.jobLog(job, fmt.Sprintf("已写入 workshop-%s 的 %d 项设置", id, len(options)))
		if err := a.restartCluster(ctx, job, c); err != nil {
			return err
		}
		return nil
	})
}

func (a *Agent) modsInstall(ctx context.Context, clusterName string, ids, collections []string, configs map[string]map[string]string) (*proto.Job, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	entries, err := a.readModsList()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !hasEntry(entries, id) {
			entries = append(entries, listEntry{Kind: "mod", ID: id, Enabled: true})
		}
	}
	for _, id := range collections {
		if !hasEntry(entries, id) {
			entries = append(entries, listEntry{Kind: "collection", ID: id, Enabled: true})
		}
	}
	if err := a.saveModsList(entries); err != nil {
		return nil, err
	}
	return a.startJob("mods.install", func(job *proto.Job) error {
		for _, id := range ids {
			a.jobLog(job, "添加模组 workshop-"+id)
		}
		for _, id := range collections {
			a.jobLog(job, "添加合集 "+id+"（会下载其中全部模组）")
		}
		// 逐模组初始设置
		for id, opts := range configs {
			if len(opts) == 0 {
				continue
			}
			if err := a.setOverridesFor(c, id, func(e *overrideEntry) {
				if e.Config == nil {
					e.Config = map[string]*luaconf.Value{}
				}
				for k, v := range opts {
					e.Config[k] = coerceScalar(v)
				}
			}); err != nil {
				return err
			}
		}
		if err := a.applyMods(ctx, job, c, "重启服务器 …"); err != nil {
			return err
		}
		return nil
	})
}

func (a *Agent) modsRemove(ctx context.Context, clusterName, id string) (*proto.Job, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	entries, err := a.readModsList()
	if err != nil {
		return nil, err
	}
	var kept []listEntry
	for _, e := range entries {
		if e.ID != id {
			kept = append(kept, e)
		}
	}
	if err := a.saveModsList(kept); err != nil {
		return nil, err
	}
	return a.startJob("mods.remove", func(job *proto.Job) error {
		a.jobLog(job, "移除模组 workshop-"+id)
		if err := a.applyMods(ctx, job, c, "重启服务器 …"); err != nil {
			return err
		}
		return nil
	})
}

func (a *Agent) modsUpdate(ctx context.Context, clusterName string, restart bool) (*proto.Job, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	return a.startJob("mods.update", func(job *proto.Job) error {
		if err := a.applyMods(ctx, job, c, map[bool]string{true: "重启服务器 …", false: "跳过重启"}[restart]); err != nil {
			return err
		}
		return nil
	})
}

// modsUpdateOne only re-downloads the given mod, then re-syncs and restarts.
func (a *Agent) modsUpdateOne(ctx context.Context, clusterName, id string, restart bool) (*proto.Job, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	return a.startJob("mods.updateOne", func(job *proto.Job) error {
		ids, err := a.enabledModIDs(ctx)
		if err != nil {
			return err
		}
		if !containsStr(ids, id) {
			return fmt.Errorf("模组 %s 没有被启用", id)
		}
		a.jobLog(job, "更新单个模组 workshop-"+id+" …")
		if err := a.downloadMods(ctx, job, []string{id}); err != nil {
			return err
		}
		if err := a.syncToCluster(ctx, job, c, ids); err != nil {
			return err
		}
		if err := a.writeModoverrides(c, ids); err != nil {
			return err
		}
		if err := a.backup(ctx, job); err != nil {
			a.jobLog(job, "警告: 备份失败: "+err.Error())
		}
		if restart {
			if err := a.restartCluster(ctx, job, c); err != nil {
				return err
			}
		} else {
			a.jobLog(job, "已跳过重启")
		}
		return nil
	})
}

// applyMods is the shared pipeline: download → sync → overrides → backup → restart.
func (a *Agent) applyMods(ctx context.Context, job *proto.Job, c clusterDef, restartNote string) error {
	ids, err := a.enabledModIDs(ctx)
	if err != nil {
		return err
	}
	if err := a.writeModsSetup(ids); err != nil {
		a.jobLog(job, "警告: 写 dedicated_server_mods_setup.lua 失败: "+err.Error())
	} else {
		a.jobLog(job, fmt.Sprintf("已生成 mods/dedicated_server_mods_setup.lua（%d 个模组）", len(ids)))
	}
	if err := a.downloadMods(ctx, job, ids); err != nil {
		return err
	}
	if err := a.syncToCluster(ctx, job, c, ids); err != nil {
		return err
	}
	if err := a.writeModoverrides(c, ids); err != nil {
		return err
	}
	a.jobLog(job, fmt.Sprintf("已写入 modoverrides.lua 到 %d 个分片（%d 个模组）", len(c.shards(a.cfg)), len(ids)))
	if err := a.backup(ctx, job); err != nil {
		a.jobLog(job, "警告: 备份失败: "+err.Error())
	}
	if restartNote == "跳过重启" {
		a.jobLog(job, "已跳过重启")
		return nil
	}
	a.jobLog(job, restartNote)
	return a.restartCluster(ctx, job, c)
}

// syncModsToCluster is used when creating a new world: sync existing mods into
// the new cluster's ugc dirs and write overrides.
func (a *Agent) syncModsToCluster(ctx context.Context, job *proto.Job, c clusterDef) error {
	ids, err := a.enabledModIDs(ctx)
	if err != nil || len(ids) == 0 {
		a.jobLog(job, "没有启用中的模组")
		return err
	}
	// 新世界目录里还没有模组缓存，需要先确认已下载
	var missing []string
	for _, id := range ids {
		if _, err := os.Stat(filepath.Join(a.cachedModDir(id), "modinfo.lua")); err != nil {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		if err := a.downloadMods(ctx, job, missing); err != nil {
			return err
		}
	}
	if err := a.syncToCluster(ctx, job, c, ids); err != nil {
		return err
	}
	if err := a.writeModoverrides(c, ids); err != nil {
		return err
	}
	a.jobLog(job, fmt.Sprintf("已把 %d 个模组同步到新世界并写好 modoverrides.lua", len(ids)))
	return nil
}

// ---- steamcmd / rsync ----

func (a *Agent) writeModsSetup(ids []string) error {
	var b strings.Builder
	b.WriteString("-- Campfire: 由面板生成（app_update validate 会重置它，所以每次更新后重建）\n")
	for _, id := range ids {
		b.WriteString(fmt.Sprintf("ServerModSetup(%q)\n", id))
	}
	path := filepath.Join(a.cfg.InstallDir, "mods", "dedicated_server_mods_setup.lua")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	_, _ = a.run.run(context.Background(), 15*time.Second, "chown", a.cfg.GameUser+":"+a.cfg.GameUser, path)
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// downloadMods runs steamcmd for the given workshop ids (skipping ones that are
// already cached and not explicitly forced).
func (a *Agent) downloadMods(ctx context.Context, job *proto.Job, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	steamcmd := filepath.Join(a.cfg.SteamCMDDir, "steamcmd.sh")
	if _, err := os.Stat(steamcmd); err != nil {
		return fmt.Errorf("找不到 steamcmd：%s", steamcmd)
	}
	args := []string{"+force_install_dir", a.cfg.InstallDir, "+login", "anonymous"}
	for _, id := range ids {
		args = append(args, "+workshop_download_item", "322330", id, "validate")
	}
	args = append(args, "+quit")
	cmdArgs := append([]string{"-u", a.cfg.GameUser, "--", steamcmd}, args...)
	cctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "runuser", cmdArgs...)
	cmd.Env = append(os.Environ(), "HOME="+a.cfg.Home)
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	pw.Close()
	var buf = make([]byte, 0, 64*1024)
	sc := newLineScanner(pr, &buf)
	okCount, failCount := 0, 0
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Success. Downloaded item") {
			okCount++
			a.jobLog(job, "下载成功: "+extractItemID(line))
		}
		if strings.Contains(line, "ERROR! Download item") || strings.Contains(line, "Failed to install") {
			failCount++
			a.jobLog(job, "下载失败: "+strings.TrimSpace(line))
		}
	}
	pr.Close()
	err = cmd.Wait()
	a.jobLog(job, fmt.Sprintf("steamcmd: 下载成功 %d 项，失败/错误标记 %d 处", okCount, failCount))
	if err != nil && okCount == 0 {
		return fmt.Errorf("steamcmd 执行失败: %w", err)
	}
	return nil
}

var itemIDRe = regexp.MustCompile(`item\s+(\d+)`)

func extractItemID(line string) string {
	if m := itemIDRe.FindStringSubmatch(line); m != nil {
		return "workshop-" + m[1]
	}
	return strings.TrimSpace(line)
}

// syncToCluster copies cached workshop content into every shard's ugc dir and
// prunes mods that are no longer enabled.
func (a *Agent) syncToCluster(ctx context.Context, job *proto.Job, c clusterDef, ids []string) error {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	synced := 0
	for _, sh := range c.shards(a.cfg) {
		contentDir := a.ugcContentDir(c, sh.Name)
		if err := os.MkdirAll(contentDir, 0o755); err != nil {
			return err
		}
		// 新世界第一次开服时，游戏会认为 "这些模组还没下载过" 而自己去 Steam
		// 重新拉一遍（大模组要几十分钟，或者直接超时失败）。把老世界的 Steam
		// UGC 状态文件（appworkshop_322330.acf）和 downloads 目录一起带过来，
		// 游戏就会直接复用我们同步好的 content。
		a.seedUgcMetadata(ctx, job, c, sh.Name)
		for _, id := range ids {
			src := a.cachedModDir(id)
			if _, err := os.Stat(filepath.Join(src, "modinfo.lua")); err != nil {
				a.jobLog(job, fmt.Sprintf("警告: %s 尚未下载（分片 %s）", id, sh.Name))
				continue
			}
			dst := a.ugcDir(c, sh.Name, id)
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			if err := a.run.chown(ctx, dst); err != nil {
				return err
			}
			if _, err := a.run.run(ctx, 10*time.Minute, "rsync", "-a", "--delete", src+"/", dst+"/"); err != nil {
				return fmt.Errorf("同步 %s 到 %s 失败: %w", id, dst, err)
			}
			synced++
		}
		// 清理不再需要的模组目录
		entries, _ := os.ReadDir(contentDir)
		pruned := 0
		for _, e := range entries {
			if !e.IsDir() || !isDigits(e.Name()) || want[e.Name()] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(contentDir, e.Name())); err != nil {
				return err
			}
			pruned++
		}
		if err := a.run.chown(ctx, contentDir); err != nil {
			return err
		}
		if pruned > 0 {
			a.jobLog(job, fmt.Sprintf("已从 %s 清理 %d 个不再需要的模组", sh.Name, pruned))
		}
	}
	a.jobLog(job, fmt.Sprintf("已同步 %d 个模组到 %d 个分片的 ugc 目录", synced/len(c.shards(a.cfg)), len(c.shards(a.cfg))))
	// 兜底自愈：某个分片缺 appworkshop_322330.acf 时会「一个模组都不注册」，
	// 从而让跨分片的客户端丢失物品栏 HUD；这里统一补齐并修正属主。
	a.ensureShardUgc(ctx, job, c)
	return nil
}

// seedUgcMetadata copies Steam's UGC bookkeeping (appworkshop_322330.acf and the
// downloads dir) from another world's shard directory. Without it, a freshly
// created world decides every mod still needs downloading and (for large
// packs) either stalls for tens of minutes or fails outright.
func (a *Agent) seedUgcMetadata(ctx context.Context, job *proto.Job, dest clusterDef, shard string) {
	destRoot := filepath.Join(a.cfg.InstallDir, "ugc_mods", dest.Name, shard)
	if _, err := os.Stat(filepath.Join(destRoot, "appworkshop_322330.acf")); err == nil {
		return // 已经有了，不用覆盖
	}
	rf, err := a.loadRegistry()
	if err != nil {
		return
	}
	for _, other := range rf.Clusters {
		if other.Name == dest.Name {
			continue
		}
		srcRoot := filepath.Join(a.cfg.InstallDir, "ugc_mods", other.Name, shard)
		if _, err := os.Stat(filepath.Join(srcRoot, "appworkshop_322330.acf")); err != nil {
			continue
		}
		if err := os.MkdirAll(destRoot, 0o755); err != nil {
			return
		}
		for _, name := range []string{"appworkshop_322330.acf", "downloads"} {
			src := filepath.Join(srcRoot, name)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			dst := filepath.Join(destRoot, name)
			if _, err := a.run.run(ctx, 5*time.Minute, "rsync", "-a", src, dst); err != nil {
				a.jobLog(job, "警告: 复制 UGC 元数据失败: "+err.Error())
			}
		}
		a.jobLog(job, "已复制 "+other.Name+"/"+shard+" 的 Steam UGC 记录（新世界不再重复下载模组）")
		return
	}
}

func (a *Agent) backup(ctx context.Context, job *proto.Job) error {
	if _, err := os.Stat(a.cfg.BackupScript); err != nil {
		return err
	}
	out, err := a.run.runAsGameUser(ctx, 5*time.Minute, "bash", a.cfg.BackupScript)
	if err != nil {
		return err
	}
	line := lastLines(strings.TrimSpace(out), 1)
	if line != "" {
		a.jobLog(job, "备份存档 "+strings.TrimSpace(line))
	} else {
		a.jobLog(job, "已备份存档")
	}
	return nil
}

func (a *Agent) restartCluster(ctx context.Context, job *proto.Job, c clusterDef) error {
	if err := a.bringUpCluster(ctx, job, c); err != nil {
		return err
	}
	for _, sh := range c.shards(a.cfg) {
		a.jobLog(job, "  "+sh.Name+" 已就绪（端口 "+strconv.Itoa(sh.Port)+"）")
	}
	a.jobLog(job, "完成：分片已按「先地上、后洞穴」的顺序重启并重新连上。")
	return nil
}

// ---- detail + image proxy ----

// modsDetail returns one mod with workshop metadata, per-shard state and the
// configuration option catalog.
func (a *Agent) modsDetail(ctx context.Context, clusterName, id string) (*proto.ModDetail, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	list, err := a.modsList(ctx, c)
	if err != nil {
		return nil, err
	}
	out := &proto.ModDetail{}
	found := false
	for _, m := range list.Mods {
		if m.ID == id {
			out.ModInfo = m
			found = true
			break
		}
	}
	if !found {
		out.ID = id
		if info, dir := a.findModinfo(c, id); info != nil {
			out.Name = info.Name
			out.Version = info.Version
			out.Author = info.Author
			out.Description = info.Desc
			out.ClientOnly = info.ClientOnly
			out.AllClients = info.AllClients
			out.Options = info.Options
			out.SizeBytes = dirSize(dir)
		}
	}
	for _, sh := range c.shards(a.cfg) {
		e, ok := a.overridesForShard(c, sh.Name)[id]
		st := proto.ShardModState{Shard: sh.Name, Enabled: ok && e.Enabled, Config: map[string]string{}}
		for k, v := range e.Config {
			st.Config[k] = choiceText(v, "")
		}
		out.ShardStates = append(out.ShardStates, st)
	}
	if details, err := a.getPublishedFileDetailsCached(ctx, []string{id}); err == nil && len(details) > 0 {
		d := details[0]
		out.WorkshopTitle = d.Title
		out.Creator = d.Creator
		out.PreviewURL = d.PreviewURL
		out.Views = d.Views
		out.CreatedAt = d.CreatedAt
		out.WorkshopUpdatedAt = d.UpdatedAt
		out.Subscriptions = d.Subscriptions
		out.Tags = d.Tags
		if out.Name == "" || strings.HasPrefix(out.Name, "workshop-") {
			out.Name = d.Title
		}
	}
	out.CatalogURL = "https://steamcommunity.com/sharedfiles/filedetails/?id=" + id
	return out, nil
}

var imageHosts = map[string]bool{
	"steamuserimages-a.akamaihd.net":  true,
	"images.steamusercontent.com":     true,
	"steamcdn-a.akamaihd.net":         true,
	"community.cloudflare.steamstatic.com": true,
	"shared.akamai.steamstatic.com":   true,
}

// fetchImage downloads a workshop preview image (whitelisted hosts only).
func (a *Agent) fetchImage(ctx context.Context, rawURL string) (*proto.ImageReply, error) {
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		return nil, fmt.Errorf("只接受 http(s) 图片地址")
	}
	host := rawURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?"); i >= 0 {
		host = host[:i]
	}
	if !imageHosts[host] {
		return nil, fmt.Errorf("不允许代理该域名：%s", host)
	}
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("图片服务器返回 %d", resp.StatusCode)
	}
	buf := make([]byte, 0, 256*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if len(buf) > 8*1024*1024 {
			return nil, fmt.Errorf("图片过大")
		}
		if err != nil {
			break
		}
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/jpeg"
	}
	return &proto.ImageReply{MIME: mime, Data: buf}, nil
}

// ensure bufio import used on all platforms
var _ = bufio.ScanLines
