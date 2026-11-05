package agent

// 世界生成参数：worldgenoverride.lua（用户可写；leveldataoverride.lua 由游戏生成，
// 绝不能手写 —— 见设计文档 §5.3.2）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"campfire/internal/luaconf"
	"campfire/internal/proto"
)

// seedLogRe 匹配 server_log.txt 里的 "World generated ... using seed: 243112468"。
var seedLogRe = regexp.MustCompile(`using seed:\s*(\d+)`)

// shardIndexLocRe 匹配存档 shardindex 里的 location = "forest" / "cave"。
var shardIndexLocRe = regexp.MustCompile(`location\s*=\s*"([^"]+)"`)

// worldgenPath is the USER-writable file for a shard of a cluster.
func (a *Agent) worldgenPath(c clusterDef, shard string) string {
	return filepath.Join(c.Dir, shard, "worldgenoverride.lua")
}

// shardIsCaves reports whether the shard is the cave (secondary) shard.
func shardIsCaves(shard string) bool { return strings.EqualFold(shard, "Caves") }

// shardLabel is the Chinese name used in user-facing messages.
func shardLabel(shard string) string {
	if shardIsCaves(shard) {
		return "洞穴"
	}
	return "地上"
}

// defaultPresetFor is the preset a shard must fall back to when nothing is set.
func defaultPresetFor(shard string) string {
	if shardIsCaves(shard) {
		return "DST_CAVE"
	}
	return "SURVIVAL_TOGETHER"
}

// isCavePreset reports whether a preset id is cave-only. Known ids come from the
// embedded catalog; unknown ids fall back to the "CAVE" naming convention every
// Klei cave preset follows (DST_CAVE, DST_CAVE_PLUS, TERRARIA_CAVE).
func isCavePreset(id string) bool {
	for _, p := range worldPresets() {
		if p.Cave && strings.EqualFold(p.ID, id) {
			return true
		}
	}
	return strings.Contains(strings.ToUpper(id), "CAVE")
}

// presetMatchesShard reports whether the preset can generate a world of the
// shard's type.
func presetMatchesShard(shard, preset string) bool {
	if shardIsCaves(shard) {
		return isCavePreset(preset)
	}
	return !isCavePreset(preset)
}

// normalizePreset fills in the shard default and rejects a preset whose world
// type does not match the shard. 这是"洞穴分片被生成成地上世界"这个故障的第一道闸门。
func normalizePreset(shard, preset string) (string, error) {
	p := strings.TrimSpace(preset)
	if p == "" {
		return defaultPresetFor(shard), nil
	}
	if !presetMatchesShard(shard, p) {
		if shardIsCaves(shard) {
			return "", fmt.Errorf("%s分片不能使用预设 %q：它不是洞穴预设，游戏会把洞穴生成成一片新的地上世界（洞穴预设：%s）",
				shardLabel(shard), p, "DST_CAVE / DST_CAVE_PLUS / TERRARIA_CAVE")
		}
		return "", fmt.Errorf("%s分片不能使用洞穴预设 %q（地上预设：SURVIVAL_TOGETHER / SURVIVAL_TOGETHER_CLASSIC / SURVIVAL_DEFAULT_PLUS / TERRARIA）",
			shardLabel(shard), p)
	}
	return p, nil
}

// forestLocationKeys are the world-gen keys the game derives from a world's
// location (forest vs cave). 一个洞穴分片只要文件里残留着"森林 location 的默认
// 值"（task_set="default" 等），游戏就会按森林任务集去生成它 —— 洞穴预设在
// 缺月亮祭坛 prefab 时无限 PANIC。这类键对洞穴分片本来就没意义：丢掉它们，
// cave location 的默认值（task_set="cave_default"、start_location="caves"…）
// 才会自动生效。
var forestLocationKeys = map[string]string{
	"task_set":        "default",
	"start_location":  "default",
	"roads":           "default",
	"layout_mode":     "LinkNodesByKeys",
	"wormhole_prefab": "wormhole",
}

// sanitizeCaveOverrides drops forest-location leftovers from a cave shard's
// settings and reports which keys were removed (no-op for the Master shard).
func sanitizeCaveOverrides(shard string, overrides map[string]string) (map[string]string, []string) {
	if !shardIsCaves(shard) || len(overrides) == 0 {
		return overrides, nil
	}
	out := make(map[string]string, len(overrides))
	var removed []string
	for k, v := range overrides {
		if want, ok := forestLocationKeys[k]; ok && v == want {
			removed = append(removed, k)
			continue
		}
		out[k] = v
	}
	sort.Strings(removed)
	return out, removed
}

// worldTypeOfSave reads the actual world type of a shard's save from its
// shardindex ("forest" / "cave"). Empty when there is no save yet.
func worldTypeOfSave(c clusterDef, shard string) string {
	data, err := os.ReadFile(filepath.Join(c.Dir, shard, "save", "shardindex"))
	if err != nil {
		return ""
	}
	if m := shardIndexLocRe.FindSubmatch(data); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

// wantWorldType is the save type the shard must produce.
func wantWorldType(shard string) string {
	if shardIsCaves(shard) {
		return "cave"
	}
	return "forest"
}

// readWorldgenOverride parses a shard's override file (missing file = no override).
func readWorldgenOverride(path string) (enabled bool, preset string, overrides map[string]string, exists bool, err error) {
	overrides = map[string]string{}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return false, "", overrides, false, nil
		}
		return false, "", overrides, false, rerr
	}
	exists = true
	chunk, perr := luaconf.Parse(string(data))
	if perr != nil {
		return false, "", overrides, exists, fmt.Errorf("解析 %s 失败: %w", filepath.Base(path), perr)
	}
	if chunk.Return == nil || chunk.Return.Kind != luaconf.KTable {
		return false, "", overrides, exists, nil
	}
	if b, ok := chunk.Return.GetBool("override_enabled"); ok {
		enabled = b
	}
	for _, key := range []string{"preset", "settings_preset", "worldgen_preset"} {
		if v := chunk.Return.Get(key); v != nil && v.Kind == luaconf.KString && v.Str != "" {
			preset = v.Str
			break
		}
	}
	if ov := chunk.Return.Get("overrides"); ov != nil && ov.Kind == luaconf.KTable {
		for _, k := range ov.Keys() {
			overrides[k] = choiceText(ov.Map[k], "")
		}
	}
	return enabled, preset, overrides, exists, nil
}

// worldgenGet returns one shard's world settings plus the embedded catalog.
func (a *Agent) worldgenGet(ctx context.Context, clusterName, shard string) (*proto.WorldgenState, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	if shard == "" {
		shard = "Master"
	}
	if shardIsCaves(shard) && !c.Caves {
		return nil, fmt.Errorf("世界 %s 没有开启洞穴分片", c.Name)
	}
	st := &proto.WorldgenState{
		Shard:         shard,
		Cluster:       c.Name,
		Overrides:     map[string]string{},
		Catalog:       worldgenCatalogData(),
		WorldType:     wantWorldType(shard),
		ExpectedPreset: defaultPresetFor(shard),
		CavesEnabled:  c.Caves,
	}
	enabled, preset, overrides, exists, err := readWorldgenOverride(a.worldgenPath(c, shard))
	if err != nil {
		return st, err
	}
	clean, leaked := sanitizeCaveOverrides(shard, overrides)
	st.HasOverrideFile = exists
	st.OverrideEnabled = enabled
	st.Overrides = clean
	st.Preset = preset
	st.PresetName = presetName(preset)
	st.WorldSeed = a.worldSeed(c, shard)
	st.PresetValid = preset != "" && presetMatchesShard(shard, preset)
	st.SaveWorldType = worldTypeOfSave(c, shard)
	st.SaveTypeMatch = st.SaveWorldType == "" || st.SaveWorldType == wantWorldType(shard)
	switch {
	case !exists:
		st.PresetHint = fmt.Sprintf("分片 %s 没有 worldgenoverride.lua：游戏会用默认关卡（地上世界）生成它。现在重新生成会得到地上世界，请先把预设设为 %s 并保存。", shard, st.ExpectedPreset)
	case !enabled:
		st.PresetHint = "worldgenoverride.lua 的 override_enabled 不是 true，游戏会忽略里面的设置。保存一次即可修正。"
	case preset == "":
		st.PresetHint = fmt.Sprintf("分片 %s 没有配置世界预设：游戏会回落到默认关卡（地上世界）。请把预设设为 %s 并保存，再重新生成。", shard, st.ExpectedPreset)
	case !st.PresetValid:
		st.PresetHint = fmt.Sprintf("分片 %s 的预设 %s 不是%s预设：重新生成会得到错误类型的世界。请改成 %s 并保存。", shard, preset, shardLabel(shard), st.ExpectedPreset)
	}
	if !st.SaveTypeMatch {
		st.PresetHint += fmt.Sprintf(" 另外：磁盘上的存档实际是 %s 类型，与 %s分片不匹配 —— 需要按正确预设重新生成。", st.SaveWorldType, shardLabel(shard))
	}
	if len(leaked) > 0 {
		st.PresetHint += fmt.Sprintf(" 另外：文件里残留了 %d 个地上世界的生成键（%s），保存或重新生成时会自动清除。",
			len(leaked), strings.Join(leaked, "、"))
	}
	return st, nil
}

// worldgenSet rewrites a shard's settings. Changes only take effect on the next
// world generation, so the UI offers "重新生成世界" separately.
//
// 返回被丢弃的键（该分片用不到的世界规则，例如地上页的野火被写进洞穴），
// 面板会把它们列出来提示用户。
func (a *Agent) worldgenSet(ctx context.Context, clusterName, shard, preset string, overrides map[string]string) ([]string, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	if shard == "" {
		shard = "Master"
	}
	if shardIsCaves(shard) && !c.Caves {
		return nil, fmt.Errorf("世界 %s 没有开启洞穴分片", c.Name)
	}
	preset, err = normalizePreset(shard, preset)
	if err != nil {
		return nil, err
	}
	dropped, err := a.writeWorldgenOverride(c, shard, preset, overrides)
	if err != nil {
		return nil, err
	}
	return dropped, nil
}

// writeWorldgenOverride writes the file (used both by the settings page and by
// world creation). The preset is always normalized/validated for the shard so a
// cave shard can never end up with a forest preset (which would silently
// generate a second overworld); settings that do not belong to the shard are
// dropped and returned to the caller.
func (a *Agent) writeWorldgenOverride(c clusterDef, shard, preset string, overrides map[string]string) ([]string, error) {
	preset, err := normalizePreset(shard, preset)
	if err != nil {
		return nil, err
	}
	overrides, dropped := filterOverridesForShard(shard, overrides)
	clean, leaked := sanitizeCaveOverrides(shard, overrides)
	if len(leaked) > 0 {
		dropped = append(dropped, leaked...)
		sort.Strings(dropped)
	}
	overrides = clean
	root := luaconf.Table()
	root.Set("override_enabled", luaconf.Bool(true))
	if preset != "" {
		root.Set("preset", luaconf.Str(preset))
	}
	ov := luaconf.Table()
	for _, k := range sortedKeys(overrides) {
		v := overrides[k]
		if v == "" || v == "default" {
			continue
		}
		ov.Set(k, coerceScalar(v))
	}
	root.Set("overrides", ov)

	path := a.worldgenPath(c, shard)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	var body []byte
	body = append(body, []byte("-- 由 Campfire 面板生成：这是“用户可写”的世界生成参数文件。\n")...)
	body = append(body, []byte("-- 注意：leveldataoverride.lua 由游戏生成，请勿手改。\n")...)
	body = append(body, []byte("return ")...)
	body = append(body, []byte(luaconf.Render(root))...)
	body = append(body, '\n')
	if err := writeFileAtomic(path, body, 0o644); err != nil {
		return nil, err
	}
	_, _ = a.run.run(context.Background(), 15*time.Second, "chown", a.cfg.GameUser+":"+a.cfg.GameUser, path)
	return dropped, nil
}

// worldSeed returns the seed of the newest generated world (empty if none): it
// is written by the game into save/shardindex.
func (a *Agent) worldSeed(c clusterDef, shard string) string {
	// 游戏只把 seed 写进 server_log.txt（"World generated ... using seed: N"），
	// 存档目录里没有，所以先看日志尾部，再退回 shardindex（旧版本可能有）。
	logPath := filepath.Join(c.Dir, shard, "server_log.txt")
	if f, err := os.Open(logPath); err == nil {
		if st, err := f.Stat(); err == nil && st.Size() > 0 {
			const tailSize = 256 << 10
			off := st.Size() - tailSize
			if off < 0 {
				off = 0
			}
			buf := make([]byte, st.Size()-off)
			if _, err := f.ReadAt(buf, off); err == nil {
				m := seedLogRe.FindAllSubmatch(buf, -1)
				if len(m) > 0 {
					f.Close()
					return string(m[len(m)-1][1])
				}
			}
		}
		f.Close()
	}
	saveDir := filepath.Join(c.Dir, shard, "save")
	if data, err := os.ReadFile(filepath.Join(saveDir, "shardindex")); err == nil {
		if seed := seedFromShardIndex(string(data)); seed != "" {
			return seed
		}
	}
	entries, err := os.ReadDir(saveDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) < 10 {
			continue
		}
		idx := filepath.Join(saveDir, e.Name(), "shardindex")
		data, err := os.ReadFile(idx)
		if err != nil {
			continue
		}
		if seed := seedFromShardIndex(string(data)); seed != "" {
			return seed
		}
	}
	return ""
}

// worldgenRegenerate backs up, moves the save aside and restarts the shards so
// the game regenerates the world with the current override file.
//
// shard == "" regenerates every shard; "Master"/"Caves" regenerates just that
// one (so fixing the caves does not destroy the overworld progress).
// 生成前会先校验每个分片的预设：不合法就直接中止、不碰任何存档 —— 否则"洞穴
// 分片缺洞穴预设"会被悄悄生成成第二片地上世界。
func (a *Agent) worldgenRegenerate(ctx context.Context, job *proto.Job, clusterName, shard string) error {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return err
	}
	all := c.shards(a.cfg)
	targets := all
	if shard != "" {
		targets = nil
		for _, s := range all {
			if strings.EqualFold(s.Name, shard) {
				targets = []Shard{s}
				break
			}
		}
		if len(targets) == 0 {
			return fmt.Errorf("世界 %s 没有分片 %s", c.Name, shard)
		}
	}
	// 预检：每个目标分片都必须能用当前配置生成对应类型的世界。
	for _, s := range targets {
		enabled, preset, overrides, exists, rerr := readWorldgenOverride(a.worldgenPath(c, s.Name))
		if rerr != nil {
			return fmt.Errorf("读取 %s 分片的世界生成配置失败：%w", s.Name, rerr)
		}
		if !exists {
			return fmt.Errorf("%s分片（%s）没有 worldgenoverride.lua：重新生成会得到默认关卡（地上世界）。请先在「世界生成」页把预设设为 %s 并保存，再重新生成。",
				shardLabel(s.Name), s.Name, defaultPresetFor(s.Name))
		}
		if !enabled {
			return fmt.Errorf("%s分片（%s）的 worldgenoverride.lua 里 override_enabled 不是 true，游戏会忽略其中的预设。请先在「世界生成」页保存一次设置。",
				shardLabel(s.Name), s.Name)
		}
		if strings.TrimSpace(preset) == "" {
			return fmt.Errorf("%s分片（%s）没有配置世界预设：现在重新生成会得到一片地上世界。请先在「世界生成」页把预设设为 %s 并保存，再重新生成。",
				shardLabel(s.Name), s.Name, defaultPresetFor(s.Name))
		}
		if !presetMatchesShard(s.Name, preset) {
			return fmt.Errorf("%s分片（%s）的预设 %s 与分片类型不匹配：重新生成会得到错误类型的世界。请先在「世界生成」页把预设改为 %s 并保存。",
				shardLabel(s.Name), s.Name, preset, defaultPresetFor(s.Name))
		}
		// 清掉历史遗留的"森林 location"键（旧版本把地上世界存进洞穴分片时，
		// 游戏会把这些值写回文件）；否则重新生成依然会 PANIC。
		if clean, leaked := sanitizeCaveOverrides(s.Name, overrides); len(leaked) > 0 {
			if _, werr := a.writeWorldgenOverride(c, s.Name, preset, clean); werr != nil {
				return fmt.Errorf("清理 %s 分片残留的地上世界生成键失败：%w", s.Name, werr)
			}
			a.jobLog(job, fmt.Sprintf("已清除 %s 分片残留的地上世界生成键：%s", shardLabel(s.Name), strings.Join(leaked, "、")))
		}
	}
	if shard == "" {
		a.jobLog(job, "预检通过：地上 / 洞穴分片的预设都匹配。")
	} else {
		a.jobLog(job, "预检通过："+shardLabel(shard)+"分片的预设匹配（只重新生成该分片）。")
	}
	stamp := time.Now().Format("20060102-150405")
	a.jobLog(job, "备份当前存档…")
	if err := a.backup(ctx, job); err != nil {
		a.jobLog(job, "备份失败（继续）: "+err.Error())
	}
	for _, sh := range targets {
		a.jobLog(job, "停止 "+sh.Service)
		if err := a.run.stopService(ctx, sh.Service); err != nil {
			return err
		}
		saveDir := filepath.Join(c.Dir, sh.Name, "save")
		if _, err := os.Stat(saveDir); err == nil {
			dest := filepath.Join(c.Dir, sh.Name, "save-"+stamp)
			a.jobLog(job, "移走旧存档 → "+sh.Name+"/save-"+stamp)
			if err := os.Rename(saveDir, dest); err != nil {
				return err
			}
			_, _ = a.run.run(ctx, 2*time.Minute, "chown", "-R", a.cfg.GameUser+":"+a.cfg.GameUser, dest)
			a.pruneSnapshots(job, c, sh.Name, snapshotKeep)
		}
	}
	a.jobLog(job, "按「先地上、后洞穴」的顺序启动，并等两个分片重新连上…")
	if err := a.bringUpCluster(ctx, job, c); err != nil {
		return err
	}
	for _, sh := range targets {
		a.jobLog(job, sh.Service+" 世界生成完成并已就绪")
		// 后验：读磁盘上存档的实际类型，确认生成的确实是该分片应有的世界。
		if wt := worldTypeOfSave(c, sh.Name); wt != "" {
			if want := wantWorldType(sh.Name); wt != want {
				return fmt.Errorf("%s分片生成的世界类型不对（磁盘上 location=%s，应为 %s）：请检查预设后重新生成", sh.Name, wt, want)
			}
			a.jobLog(job, fmt.Sprintf("校验通过：%s 存档类型 location=%s（%s世界）", sh.Name, wt, shardLabel(sh.Name)))
		}
	}
	if shard == "" {
		a.jobLog(job, "完成：新世界已按当前设置生成（地上 + 洞穴）。")
	} else {
		a.jobLog(job, "完成："+shardLabel(shard)+"分片已按当前设置重新生成，其它分片未改动。")
	}
	return nil
}
