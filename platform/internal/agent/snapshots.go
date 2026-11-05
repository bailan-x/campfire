package agent

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

// Legacy shard restarts used to start Master and Caves at the same time. Klei's
// server listing needs the Master row to be registered before a secondary shard
// can find it, so every (re)start goes through bringUpCluster: Master first,
// wait for "Server registered via geo DNS", then Caves, then wait for the two
// shards to see each other.
const snapshotPrefix = "save-"

// snapshotKeep is how many historical save directories are kept per shard.
const snapshotKeep = 2

var (
	reRegistered    = regexp.MustCompile(`Server registered via geo DNS`)
	reCaveConnected = regexp.MustCompile(`World \d+\(Caves\) is now connected`)
	reCaveReady     = regexp.MustCompile(`secondary shard is now ready`)
	reRowidConflict = regexp.MustCompile(`E_ROWID_EXIST`)
	reSimPaused     = regexp.MustCompile(`Sim paused`)
	reRegisteringMod = regexp.MustCompile(`Registering Mod workshop-(\d+)`)
	reShardIndexLoc = regexp.MustCompile(`location\s*[:=]\s*"([^"]+)"`)
	reShardIndexSes = regexp.MustCompile(`session[_a-z]*\s*[:=]\s*"([^"]+)"`)
)

func shardLogPath(c clusterDef, shard string) string {
	return filepath.Join(c.Dir, shard, "server_log.txt")
}

// snapshotDirs lists the save-<stamp> directories of one shard, newest first.
func snapshotDirs(c clusterDef, shard string) []string {
	entries, err := os.ReadDir(filepath.Join(c.Dir, shard))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), snapshotPrefix) {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func isUpperHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return s != ""
}

// shardSessionID finds the DST session folder (16 upper-case hex chars) inside a
// save directory.
func shardSessionID(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 16 && isUpperHex(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

// snapshotMeta describes one historical save directory.
func (a *Agent) snapshotMeta(c clusterDef, shard, name string) proto.Snapshot {
	dir := filepath.Join(c.Dir, shard, name)
	sn := proto.Snapshot{Shard: shard, Name: name}
	// The directory name carries the moment the snapshot was taken; cp -a
	// keeps the save's own mtime, which would be misleading in the table.
	if t, err := time.ParseInLocation("20060102-150405", strings.TrimPrefix(name, snapshotPrefix), time.Local); err == nil {
		sn.Modified = t.Unix()
	} else if st, err := os.Stat(dir); err == nil {
		sn.Modified = st.ModTime().Unix()
	}
	sn.SizeBytes = dirSize(dir)
	sn.SessionID = shardSessionID(dir)
	if data, err := os.ReadFile(filepath.Join(dir, "shardindex")); err == nil {
		src := string(data)
		if m := reShardIndexLoc.FindStringSubmatch(src); m != nil {
			sn.WorldType = m[1]
		}
		if m := reShardIndexSes.FindStringSubmatch(src); m != nil {
			sn.SessionID = m[1]
		}
		if seed := seedFromShardIndex(src); seed != "" {
			sn.Seed = seed
		}
	}
	return sn
}

// pruneSnapshots deletes all but the newest keep save-<stamp> directories.
// Names in exclude are always kept (e.g. the snapshot being restored).
func (a *Agent) pruneSnapshots(job *proto.Job, c clusterDef, shard string, keep int, exclude ...string) {
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[e] = true
	}
	kept := 0
	for _, name := range snapshotDirs(c, shard) {
		if skip[name] {
			kept++
			continue
		}
		path := filepath.Join(c.Dir, shard, name)
		// A snapshot directory with nothing in it is broken (the move was
		// interrupted); it must not occupy one of the slots.
		if dirSize(path) == 0 {
			if err := os.RemoveAll(path); err != nil {
				a.run.logf("prune %s: %v", path, err)
				continue
			}
			if job != nil {
				a.jobLog(job, "清理空快照 "+shard+"/"+name)
			}
			continue
		}
		if kept < keep {
			kept++
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			a.run.logf("prune %s: %v", path, err)
			continue
		}
		if job != nil {
			a.jobLog(job, "清理旧存档 "+shard+"/"+name)
		}
	}
}

// ---- ops ----

// worldgenSnapshots lists the historical saves of every shard.
func (a *Agent) worldgenSnapshots(ctx context.Context, clusterName string) (*proto.SnapshotList, error) {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return nil, err
	}
	out := &proto.SnapshotList{Cluster: c.Name, Keep: snapshotKeep}
	for _, sh := range c.shards(a.cfg) {
		for _, name := range snapshotDirs(c, sh.Name) {
			out.Snapshots = append(out.Snapshots, a.snapshotMeta(c, sh.Name, name))
		}
	}
	return out, nil
}

// createSnapshot copies the current save of each shard into a save-<stamp>
// directory (a manual restore point), then prunes old ones.
func (a *Agent) createSnapshot(ctx context.Context, job *proto.Job, clusterName string) error {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	for _, sh := range c.shards(a.cfg) {
		save := filepath.Join(c.Dir, sh.Name, "save")
		if st, err := os.Stat(save); err != nil || !st.IsDir() {
			a.jobLog(job, sh.Name+"：还没有存档，跳过")
			continue
		}
		dst := filepath.Join(c.Dir, sh.Name, snapshotPrefix+stamp)
		if _, err := os.Stat(dst); err == nil {
			dst += "-b"
		}
		a.jobLog(job, "创建存档快照 "+sh.Name+"/"+filepath.Base(dst))
		if out, err := a.run.run(ctx, 10*time.Minute, "cp", "-a", save, dst); err != nil {
			return fmt.Errorf("创建 %s 存档快照失败: %w (%s)", sh.Name, err, strings.TrimSpace(out))
		}
		_, _ = a.run.run(ctx, 5*time.Minute, "chown", "-R", a.cfg.GameUser+":"+a.cfg.GameUser, dst)
		a.pruneSnapshots(job, c, sh.Name, snapshotKeep)
	}
	a.jobLog(job, "完成：已创建存档快照（每个分片保留最近 "+strconv.Itoa(snapshotKeep)+" 份）。")
	return nil
}

// worldgenRestore puts a save-<stamp> directory back as the live save of one
// shard, keeps the replaced save as a pre-restore snapshot, then restarts the
// cluster in the correct order.
func (a *Agent) worldgenRestore(ctx context.Context, job *proto.Job, clusterName, shardRaw, name string) error {
	c, err := a.resolveCluster(clusterName)
	if err != nil {
		return err
	}
	sh, err := a.pickShard(c, shardRaw)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(name, snapshotPrefix) || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("存档快照名不合法: %q", name)
	}
	src := filepath.Join(c.Dir, sh.Name, name)
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return fmt.Errorf("找不到存档快照 %s/%s", sh.Name, name)
	}

	a.jobLog(job, "回档目标："+sh.Name+"/"+name)
	a.jobLog(job, "备份当前存档…")
	if err := a.backup(ctx, job); err != nil {
		a.jobLog(job, "  备份失败（继续）: "+err.Error())
	}

	for _, s := range c.shards(a.cfg) {
		if err := a.run.stopService(ctx, s.Service); err != nil {
			a.jobLog(job, "  停止 "+s.Service+" 失败: "+err.Error())
		}
	}

	stamp := time.Now().Format("20060102-150405")
	save := filepath.Join(c.Dir, sh.Name, "save")
	if _, err := os.Stat(save); err == nil {
		aside := filepath.Join(c.Dir, sh.Name, "pre-restore-"+stamp)
		a.jobLog(job, "把当前存档挪到 "+sh.Name+"/"+filepath.Base(aside))
		if err := os.Rename(save, aside); err != nil {
			return err
		}
		_, _ = a.run.run(ctx, 3*time.Minute, "chown", "-R", a.cfg.GameUser+":"+a.cfg.GameUser, aside)
	}

	a.jobLog(job, "复制快照 "+name+" → "+sh.Name+"/save")
	if out, err := a.run.run(ctx, 10*time.Minute, "cp", "-a", src, save); err != nil {
		return fmt.Errorf("复制存档快照失败: %w (%s)", err, strings.TrimSpace(out))
	}
	_, _ = a.run.run(ctx, 5*time.Minute, "chown", "-R", a.cfg.GameUser+":"+a.cfg.GameUser, save)
	a.pruneSnapshots(job, c, sh.Name, snapshotKeep, name)

	a.jobLog(job, "重新启动分片…")
	if err := a.bringUpCluster(ctx, job, c); err != nil {
		return err
	}
	if wt := worldTypeOfSave(c, sh.Name); wt != "" {
		a.jobLog(job, fmt.Sprintf("回档完成：%s 存档类型 location=%s", sh.Name, wt))
	}
	a.jobLog(job, "完成：已回档到 "+name+"。")
	return nil
}

// pickShard resolves a shard name ("" => Master).
func (a *Agent) pickShard(c clusterDef, name string) (Shard, error) {
	shards := c.shards(a.cfg)
	if strings.TrimSpace(name) == "" {
		if len(shards) == 0 {
			return Shard{}, fmt.Errorf("世界 %s 没有分片", c.Name)
		}
		return shards[0], nil
	}
	for _, s := range shards {
		if strings.EqualFold(s.Name, name) {
			return s, nil
		}
	}
	return Shard{}, fmt.Errorf("世界 %s 没有分片 %s", c.Name, name)
}

// bringUpCluster restarts the shards in the only order DST accepts: Caves down,
// Master up and registered, then Caves up, then both shards linked.
func (a *Agent) bringUpCluster(ctx context.Context, job *proto.Job, c clusterDef) error {
	shards := c.shards(a.cfg)
	if len(shards) == 0 {
		return fmt.Errorf("世界 %s 没有分片", c.Name)
	}
	master := shards[0]
	var cave *Shard
	for i := range shards {
		if shardIsCaves(shards[i].Name) {
			cave = &shards[i]
		}
	}

	// 先停：Caves 先停，Master 后停。优先让游戏自己 c_shutdown(true) 优雅关服——
	// 它会存档并向 Klei 注销列表行，这正是下一次启动不再撞 E_ROWID_EXIST 的关键；
	// 控制台不可用（老启动方式）时退回 systemctl stop。已经在停的分片不再重复打印。
	if cave != nil {
		if err := a.gracefulStopShard(ctx, job, c, *cave, 120*time.Second); err != nil {
			return err
		}
	}
	masterWasActive := false
	if active, _ := a.run.serviceState(ctx, master.Service); active {
		masterWasActive = true
		if err := a.gracefulStopShard(ctx, job, c, master, 120*time.Second); err != nil {
			return err
		}
	}
	wait := 2 * time.Second
	if masterWasActive {
		wait = 5 * time.Second
		a.jobLog(job, "优雅关服完成；等 5 秒再启动，避开列表登记行切换")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
	}

	masterLog := shardLogPath(c, master.Name)
	// 每次起服前先自愈分片的 Steam UGC 记录：缺 appworkshop_322330.acf / downloads
	// 的分片会认为所有模组都没下载过，去 Steam 拉一遍超时后「一个模组都不注册」，
	// 于是客户端在分片间迁移时净变量布局不一致，物品栏/合成栏直接消失。
	a.ensureShardUgc(ctx, job, c)
	masterOff := fileSize(masterLog)
	offline := clusterOffline(c)
	if offline {
		a.jobLog(job, "启动 "+master.Service+"（该世界为「不登记公共列表」模式，不做 Klei 注册）")
	} else {
		a.jobLog(job, "启动 "+master.Service+"，等它注册到 Klei 服务器列表")
	}
	if err := a.run.startService(ctx, master.Service); err != nil {
		return err
	}
	masterSince := time.Now().Add(-2 * time.Second)
	if offline {
		// 直连模式下日志里没有 "Online Server Started on port:"，只能等 "Sim paused"。
		if _, err := a.run.waitLogLine(ctx, masterLog, masterOff, reSimPaused, 8*time.Minute); err != nil {
			return fmt.Errorf("主分片已启动，但没等到它进入就绪状态：%v", err)
		}
		a.jobLog(job, "  分片已就绪（直连模式，朋友用 c_connect 地址进入）")
	} else {
		line, registered, err := a.waitRegistered(ctx, masterLog, masterOff, 15*time.Minute, job)
		if err != nil {
			return err
		}
		if !registered {
			return fmt.Errorf("主分片已经起来了，但没能确认它注册到 Klei 服务器列表")
		}
		a.jobLog(job, "  "+line)
		if err := a.run.waitStarted(ctx, master.Service, masterSince, 3*time.Minute); err != nil {
			a.jobLog(job, "  警告: 没等到分片进入暂停就绪状态: "+err.Error())
		}
	}
	if cave == nil {
		return nil
	}

	caveLog := shardLogPath(c, cave.Name)
	linkOff := fileSize(masterLog)
	caveSince := time.Now().Add(-2 * time.Second)
	a.jobLog(job, "启动 "+cave.Service+"（等两个分片握手）")
	if err := a.run.startService(ctx, cave.Service); err != nil {
		return err
	}
	if !offline {
		if err := a.run.waitStarted(ctx, cave.Service, caveSince, 10*time.Minute); err != nil {
			return err
		}
	}
	if line, err := a.run.waitLogLine(ctx, masterLog, linkOff, reCaveConnected, 8*time.Minute); err != nil {
		return fmt.Errorf("洞穴分片已启动，但两个分片没有连上（%v）。这通常是主分片的服务器列表注册还没真正生效，稍后重试「重新连接分片」", err)
	} else {
		a.jobLog(job, "  "+line)
	}
	// 洞穴日志每次启动都会被重写：用「起始时间之后的 mtime」判断，避免旧文件的
	// 偏移量抢在新文件前面把这一行跳过去（曾导致任务卡满 8 分钟并误报警告）。
	if line, err := a.run.waitLogLineNew(ctx, caveLog, reCaveReady, caveSince, 3*time.Minute); err != nil {
		a.jobLog(job, "  警告: 洞穴分片没有报告就绪: "+err.Error())
	} else {
		a.jobLog(job, "  "+line)
	}
	a.verifyShardModParity(ctx, job, c)
	return nil
}

// gracefulStopShard stops one shard through its own console: c_shutdown(true)
// makes DST save the world, tell the Klei listing to drop the server row and exit
// normally. That clean deregistration is what a systemctl stop (SIGTERM) does NOT
// guarantee, and a leftover listing row is exactly what makes the next start fail
// with E_ROWID_EXIST. Falls back to systemctl stop if the console is unavailable
// (e.g. the unit still uses the pre-0.2.8 start command) or the shard ignores the
// request within timeout.
func (a *Agent) gracefulStopShard(ctx context.Context, job *proto.Job, c clusterDef, sh Shard, timeout time.Duration) error {
	active, _ := a.run.serviceState(ctx, sh.Service)
	if !active {
		return nil
	}
	a.jobLog(job, "停止 "+sh.Service+"（先让游戏自己存档并注销列表）")
	if _, err := a.consoleExec(ctx, proto.ConsoleExecParams{Cluster: c.Name, Shard: sh.Name, Command: "c_shutdown(true)"}); err != nil {
		a.jobLog(job, "  控制台关服不可用（"+err.Error()+"），改用 systemctl stop")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if active, _ := a.run.serviceState(ctx, sh.Service); !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	a.jobLog(job, "  分片没有在预期时间内自己退出，强制 systemctl stop")
	return a.run.stopService(ctx, sh.Service)
}

// clusterOffline reports whether the cluster runs in "offline" mode, i.e.
// cluster.ini has [NETWORK] offline_cluster = true. Such a server never talks to
// the Klei server browser, so it can never hit the E_ROWID_EXIST listing
// conflict — but it also never prints "Server registered via geo DNS", so the
// restart flow must not wait for that line.
func clusterOffline(c clusterDef) bool {
	data, err := os.ReadFile(filepath.Join(c.Dir, "cluster.ini"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "offline_cluster") {
			continue
		}
		if i := strings.Index(line, "="); i >= 0 {
			return strings.EqualFold(strings.TrimSpace(line[i+1:]), "true")
		}
	}
	return false
}

// waitRegistered watches the master shard log for a successful Klei listing
// registration. A previous unclean shutdown (or a stop/start in the same minute)
// can leave the old listing row behind, in which case Klei answers E_ROWID_EXIST
// until the stale row expires server-side; the shard keeps retrying every few
// seconds and registers by itself once the row is gone, so this keeps waiting
// for the whole timeout instead of giving up on the first conflicts.
func (a *Agent) waitRegistered(ctx context.Context, logPath string, offset int64, timeout time.Duration, job *proto.Job) (string, bool, error) {
	deadline := time.Now().Add(timeout)
	rowid := 0
	noted := false
	nextNote := time.Now().Add(3 * time.Minute)
	for {
		text, newOff, err := readNewBytes(logPath, offset)
		if err == nil {
			offset = newOff
			if m := reRegistered.FindString(text); m != "" {
				return m, true, nil
			}
			if n := len(reRowidConflict.FindAllString(text, -1)); n > 0 {
				rowid += n
				if !noted {
					noted = true
					a.jobLog(job, "  Klei 返回 E_ROWID_EXIST：列表里还留着上一次的登记行，分片会自动重试，通常等几分钟就能登记上")
				}
			}
		}
		now := time.Now()
		if now.After(nextNote) && rowid > 0 {
			nextNote = now.Add(3 * time.Minute)
			left := int(deadline.Sub(now).Minutes())
			a.jobLog(job, fmt.Sprintf("  仍在等 Klei 释放旧登记行（已重试 %d 次，最多再等 %d 分钟）", rowid, left))
		}
		if now.After(deadline) {
			if rowid > 0 {
				return "", false, fmt.Errorf("等了 %s，Klei 仍然返回 E_ROWID_EXIST（%d 次重试）。说明上一次的服务器登记行没有释放：请停掉仍在用同一个 Klei Token 的其它世界或电脑上开的服，或者在 accounts.klei.com 给这个服务器换一个新 Token 后重试（存档不受影响）", timeout, rowid)
			}
			return "", false, fmt.Errorf("等了 %s 也没看到「Server registered via geo DNS」", timeout)
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
	}
}

// ensureShardUgc makes sure every shard of a cluster has Steam's UGC bookkeeping
// (appworkshop_322330.acf plus the downloads/ and temp/ dirs) and that the shard's
// ugc root belongs to the game user. A shard missing the acf believes none of the
// mods were ever downloaded: it asks Steam for all of them, times out
// ("DownloadServerMods timed out") and then registers ZERO mods. Clients that
// migrate between such a shard and a normal one see a different netvar layout and
// lose their inventory/crafting HUD, so this self-heal runs on every start.
func (a *Agent) ensureShardUgc(ctx context.Context, job *proto.Job, c clusterDef) {
	shards := c.shards(a.cfg)
	for _, sh := range shards {
		root := filepath.Join(a.cfg.InstallDir, "ugc_mods", c.Name, sh.Name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			a.jobLog(job, fmt.Sprintf("警告: 创建 %s 分片 ugc 目录失败: %v", sh.Name, err))
			continue
		}
		acf := filepath.Join(root, "appworkshop_322330.acf")
		if _, err := os.Stat(acf); err != nil {
			for _, other := range shards {
				if other.Name == sh.Name {
					continue
				}
				srcRoot := filepath.Join(a.cfg.InstallDir, "ugc_mods", c.Name, other.Name)
				if _, err := os.Stat(filepath.Join(srcRoot, "appworkshop_322330.acf")); err != nil {
					continue
				}
				fixed := true
				for _, name := range []string{"appworkshop_322330.acf", "downloads", "temp"} {
					src := filepath.Join(srcRoot, name)
					if _, err := os.Stat(src); err != nil {
						continue
					}
					dst := filepath.Join(root, name)
					if _, err := a.run.run(ctx, 5*time.Minute, "rsync", "-a", src, dst); err != nil {
						a.jobLog(job, fmt.Sprintf("警告: 复制 %s 分片的 UGC 记录到 %s 失败: %v", other.Name, sh.Name, err))
						fixed = false
					}
				}
				if fixed {
					a.jobLog(job, fmt.Sprintf("已修复 %s 分片缺失的 Steam UGC 记录（从 %s 分片复制），该分片无需重新下载模组", sh.Name, other.Name))
				}
				break
			}
		}
		if err := a.run.chown(ctx, root); err != nil {
			a.jobLog(job, fmt.Sprintf("警告: 修正 %s 分片 ugc 目录属主失败: %v", sh.Name, err))
		}
	}
}

// verifyShardModParity compares the set of mods each shard actually registered in
// its current run. Different sets mean every client that migrates between the
// shards reads entities with a shifted netvar layout (inventory/crafting HUD
// disappears), so a mismatch is reported loudly instead of failing silently.
func (a *Agent) verifyShardModParity(ctx context.Context, job *proto.Job, c clusterDef) {
	type shardMods struct {
		name string
		ids  map[string]bool
	}
	var all []shardMods
	for _, sh := range c.shards(a.cfg) {
		sh := sh
		data, _, err := readNewBytes(shardLogPath(c, sh.Name), 0)
		ids := map[string]bool{}
		if err == nil {
			for _, m := range reRegisteringMod.FindAllStringSubmatch(data, -1) {
				ids[m[1]] = true
			}
		}
		all = append(all, shardMods{name: sh.Name, ids: ids})
	}
	if len(all) < 2 {
		return
	}
	base := all[0]
	mismatch := false
	for _, other := range all[1:] {
		if len(other.ids) != len(base.ids) {
			mismatch = true
		}
		for id := range base.ids {
			if !other.ids[id] {
				mismatch = true
			}
		}
		for id := range other.ids {
			if !base.ids[id] {
				mismatch = true
			}
		}
	}
	if !mismatch {
		a.jobLog(job, fmt.Sprintf("  模组一致性检查通过：各分片都注册了 %d 个模组", len(base.ids)))
		return
	}
	var parts []string
	for _, s := range all {
		list := make([]string, 0, len(s.ids))
		for id := range s.ids {
			list = append(list, id)
		}
		sort.Strings(list)
		parts = append(parts, fmt.Sprintf("%s=%d[%s]", s.name, len(list), strings.Join(list, ",")))
	}
	a.jobLog(job, "  警告: 两个分片注册的模组不一致（"+strings.Join(parts, " / ")+"）")
	a.jobLog(job, "        这会让玩家在分片间迁移时丢失物品栏/合成界面；请检查各分片 ugc 目录下的 appworkshop_322330.acf 是否都在，或重新保存一次模组设置。")
}
