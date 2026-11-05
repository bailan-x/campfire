package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"campfire/internal/proto"
)

// Shard is one DST shard (Master or Caves).
type Shard struct {
	Name    string
	Service string
	Port    int
}

// Config is the agent's view of the deployment.
type Config struct {
	Sock          string
	ClusterDir    string // .../DoNotStarveTogether/MyDediServer
	InstallDir    string // .../dst-server
	SteamCMDDir   string // .../steamcmd
	GameUser      string
	Group         string // unix group allowed to talk to the socket (default campfire)
	Home          string
	ModsList      string
	BackupScript  string
	Shards        []Shard
	Version       string
}

// Agent serves RPC requests over a unix socket.
type Agent struct {
	cfg  Config
	run  *runner
	jobsMu sync.Mutex
	jobs map[string]*proto.Job
	seq  int64
}

// New builds an agent from config, filling sane defaults.
func New(cfg Config) *Agent {
	if cfg.GameUser == "" {
		cfg.GameUser = "dst"
	}
	if cfg.Group == "" {
		cfg.Group = "campfire"
	}
	if cfg.Home == "" {
		cfg.Home = "/home/dst"
	}
	if cfg.ModsList == "" {
		cfg.ModsList = filepath.Join(cfg.Home, "mods.list")
	}
	if cfg.BackupScript == "" {
		cfg.BackupScript = filepath.Join(cfg.Home, "dst-backup.sh")
	}
	if cfg.SteamCMDDir == "" {
		cfg.SteamCMDDir = filepath.Join(cfg.Home, "steamcmd")
	}
	if cfg.Sock == "" {
		cfg.Sock = "/run/campfire/agent.sock"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if len(cfg.Shards) == 0 {
		cfg.Shards = []Shard{
			{Name: "Master", Service: "dst-server", Port: 10999},
			{Name: "Caves", Service: "dst-server-caves", Port: 10998},
		}
	}
	return &Agent{cfg: cfg, run: &runner{GameUser: cfg.GameUser, Home: cfg.Home}, jobs: map[string]*proto.Job{}}
}

// Serve listens on the unix socket and serves until ctx is done.
func (a *Agent) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(a.cfg.Sock), 0o755); err != nil {
		return err
	}
	// Console support (stdin wrapper + unit migration) has to be in place before
	// the panel runs its first command. Running shards pick it up on restart.
	a.bootstrapConsole(ctx)
	_ = os.Remove(a.cfg.Sock)
	ln, err := net.Listen("unix", a.cfg.Sock)
	if err != nil {
		return err
	}
	defer ln.Close()
	if err := os.Chmod(a.cfg.Sock, 0o660); err != nil {
		a.run.logf("chmod socket: %v", err)
	}
	// Make the socket usable by the panel's SSH login user (ops) and the game user.
	_, _ = a.run.run(ctx, 10*time.Second, "chgrp", a.cfg.Group, a.cfg.Sock)
	a.run.logf("listening on %s", a.cfg.Sock)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		go a.handleConn(ctx, conn)
	}
}

func (a *Agent) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req proto.Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(proto.Response{OK: false, Error: "bad request: " + err.Error()})
			continue
		}
		resp := a.dispatch(ctx, req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (a *Agent) dispatch(ctx context.Context, req proto.Request) proto.Response {
	resp := proto.Response{ID: req.ID}
	var (
		res    interface{}
		err    error
	)
	switch req.Op {
	case "ping":
		res = map[string]interface{}{"version": a.cfg.Version, "time": time.Now().Unix()}
	case "status":
		res, err = a.status(ctx)

	// ---- 世界（cluster） ----
	case "clusters.list":
		res, err = a.clustersList(ctx)
	case "clusters.activate":
		var p struct{ Name string `json:"name"` }
		err = decode(req.Params, &p)
		if err == nil {
			err = a.activateCluster(p.Name)
			if err == nil {
				res = map[string]bool{"ok": true}
			}
		}
	case "clusters.create":
		var p proto.CreateWorldParams
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.startJob("clusters.create", func(j *proto.Job) error {
				return a.createWorld(ctx, j, p)
			})
		}
	case "clusters.start":
		var p struct{ Name string `json:"name"` }
		err = decode(req.Params, &p)
		if err == nil {
			var c clusterDef
			c, err = a.clusterByName(p.Name)
			if err == nil {
				res, err = a.startJob("clusters.start", func(j *proto.Job) error {
					return a.startCluster(ctx, j, c)
				})
			}
		}
	case "clusters.stop":
		var p struct{ Name string `json:"name"` }
		err = decode(req.Params, &p)
		if err == nil {
			var c clusterDef
			c, err = a.clusterByName(p.Name)
			if err == nil {
				res, err = a.startJob("clusters.stop", func(j *proto.Job) error {
					return a.stopCluster(ctx, j, c)
				})
			}
		}
	case "clusters.remove":
		var p struct{ Name string `json:"name"` }
		err = decode(req.Params, &p)
		if err == nil {
			name := p.Name
			res, err = a.startJob("clusters.remove", func(j *proto.Job) error {
				return a.removeWorld(ctx, j, name)
			})
		}

	// ---- 模组 ----
	case "mods.list":
		var p struct{ Cluster string `json:"cluster"` }
		err = decode(req.Params, &p)
		if err == nil {
			var c clusterDef
			c, err = a.resolveCluster(p.Cluster)
			if err == nil {
				res, err = a.modsList(ctx, c)
			}
		}
	case "mods.detail":
		var p struct {
			Cluster string `json:"cluster"`
			ID      string `json:"id"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.modsDetail(ctx, p.Cluster, p.ID)
		}
	case "mods.setEnabled":
		var p struct {
			Cluster string `json:"cluster"`
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.modsSetEnabled(ctx, p.Cluster, p.ID, p.Enabled)
		}
	case "mods.setConfig":
		var p struct {
			Cluster string            `json:"cluster"`
			ID      string            `json:"id"`
			Options map[string]string `json:"options"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.modsSetConfig(ctx, p.Cluster, p.ID, p.Options)
		}
	case "mods.install":
		var p struct {
			Cluster     string                       `json:"cluster"`
			IDs         []string                     `json:"ids"`
			Collections []string                     `json:"collections"`
			Config      map[string]map[string]string `json:"config"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.modsInstall(ctx, p.Cluster, p.IDs, p.Collections, p.Config)
		}
	case "mods.remove":
		var p struct {
			Cluster string `json:"cluster"`
			ID      string `json:"id"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.modsRemove(ctx, p.Cluster, p.ID)
		}
	case "mods.update":
		var p struct {
			Cluster string `json:"cluster"`
			Restart bool   `json:"restart"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.modsUpdate(ctx, p.Cluster, p.Restart)
		}
	case "mods.updateOne":
		var p struct {
			Cluster string `json:"cluster"`
			ID      string `json:"id"`
			Restart bool   `json:"restart"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.modsUpdateOne(ctx, p.Cluster, p.ID, p.Restart)
		}

	// ---- 创意工坊 ----
	case "workshop.lookup":
		var p struct{ Input string `json:"input"` }
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.workshopLookup(ctx, p.Input)
		}
	case "workshop.resolve":
		var p struct {
			IDs         []string `json:"ids"`
			Collections []string `json:"collections"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.workshopResolve(ctx, p.IDs, p.Collections)
		}
	case "workshop.image":
		var p struct{ URL string `json:"url"` }
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.fetchImage(ctx, p.URL)
		}

	// ---- 世界生成 ----
	case "worldgen.get":
		var p struct {
			Cluster string `json:"cluster"`
			Shard   string `json:"shard"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.worldgenGet(ctx, p.Cluster, p.Shard)
		}
	case "worldgen.set":
		var p struct {
			Cluster   string            `json:"cluster"`
			Shard     string            `json:"shard"`
			Preset    string            `json:"preset"`
			Overrides map[string]string `json:"overrides"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			var dropped []string
			dropped, err = a.worldgenSet(ctx, p.Cluster, p.Shard, p.Preset, p.Overrides)
			if err == nil {
				res = map[string]any{"ok": true, "dropped": dropped}
			}
		}
	case "worldgen.regenerate":
		var p struct {
			Cluster string `json:"cluster"`
			Shard   string `json:"shard"` // "" = 全部分片；Master / Caves = 只重新生成该分片
		}
		err = decode(req.Params, &p)
		if err == nil {
			cluster, shard := p.Cluster, p.Shard
			res, err = a.startJob("worldgen.regenerate", func(j *proto.Job) error {
				return a.worldgenRegenerate(ctx, j, cluster, shard)
			})
		}

	case "worldgen.snapshots":
		var p struct {
			Cluster string `json:"cluster"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.worldgenSnapshots(ctx, p.Cluster)
		}
	case "worldgen.snapshot":
		var p struct {
			Cluster string `json:"cluster"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			cluster := p.Cluster
			res, err = a.startJob("worldgen.snapshot", func(j *proto.Job) error {
				return a.createSnapshot(ctx, j, cluster)
			})
		}
	case "worldgen.restore":
		var p struct {
			Cluster string `json:"cluster"`
			Shard   string `json:"shard"`
			Name    string `json:"name"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			cluster, shard, name := p.Cluster, p.Shard, p.Name
			res, err = a.startJob("worldgen.restore", func(j *proto.Job) error {
				return a.worldgenRestore(ctx, j, cluster, shard, name)
			})
		}

	// ---- 任务 / 日志 / 服务 ----
	case "console.exec":
		var p proto.ConsoleExecParams
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.consoleExec(ctx, p)
		}
	case "jobs.get":
		var p struct{ ID string `json:"id"` }
		err = decode(req.Params, &p)
		if err == nil {
			res, err = a.getJob(p.ID)
		}
	case "jobs.list":
		res = a.listJobs()
	case "logs.tail":
		var p struct {
			Cluster string `json:"cluster"`
			Service string `json:"service"`
			Lines   int    `json:"lines"`
		}
		err = decode(req.Params, &p)
		if err == nil {
			var c clusterDef
			c, err = a.resolveCluster(p.Cluster)
			if err == nil {
				unit := p.Service
				if unit == "" {
					unit = c.MasterUnit
				}
				res, err = a.logsTail(ctx, unit, p.Lines)
			}
		}
	case "service.restart":
		var p struct{ Cluster string `json:"cluster"` }
		err = decode(req.Params, &p)
		if err == nil {
			var c clusterDef
			c, err = a.resolveCluster(p.Cluster)
			if err == nil {
				res, err = a.startJob("service.restart", func(j *proto.Job) error {
					return a.restartCluster(ctx, j, c)
				})
			}
		}
	default:
		err = fmt.Errorf("unknown op %q", req.Op)
	}
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		return resp
	}
	raw, mErr := json.Marshal(res)
	if mErr != nil {
		resp.OK = false
		resp.Error = mErr.Error()
		return resp
	}
	resp.OK = true
	resp.Result = raw
	return resp
}

func decode(raw json.RawMessage, dst interface{}) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// ---- jobs ----

func (a *Agent) startJob(kind string, fn func(*proto.Job) error) (*proto.Job, error) {
	id := fmt.Sprintf("%d", atomic.AddInt64(&a.seq, 1))
	job := &proto.Job{ID: id, Kind: kind, State: "running", Started: time.Now().Unix(), Log: []string{}}
	a.jobsMu.Lock()
	a.jobs[id] = job
	a.jobsMu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.jobLog(job, fmt.Sprintf("内部错误: %v", r))
				a.jobDone(job, fmt.Errorf("internal error: %v", r))
			}
		}()
		err := fn(job)
		a.jobDone(job, err)
	}()
	return job, nil
}

func (a *Agent) jobLog(job *proto.Job, line string) {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	job.Log = append(job.Log, line)
	if len(job.Log) > 800 {
		job.Log = job.Log[len(job.Log)-800:]
	}
	a.run.logf("[job %s] %s", job.ID, line)
}

func (a *Agent) jobDone(job *proto.Job, err error) {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	job.Ended = time.Now().Unix()
	if err != nil {
		job.State = "failed"
		job.Error = err.Error()
		return
	}
	job.State = "done"
}

func (a *Agent) getJob(id string) (*proto.Job, error) {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	job, ok := a.jobs[id]
	if !ok {
		return nil, fmt.Errorf("job %s not found", id)
	}
	cp := *job
	cp.Log = append([]string{}, job.Log...)
	return &cp, nil
}

func (a *Agent) listJobs() []proto.Job {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	out := make([]proto.Job, 0, len(a.jobs))
	for _, j := range a.jobs {
		cp := *j
		cp.Log = append([]string{}, j.Log...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Started < out[k].Started })
	return out
}

// ---- status ----

func (a *Agent) status(ctx context.Context) (*proto.Status, error) {
	c, err := a.activeCluster()
	if err != nil {
		return nil, err
	}
	st := &proto.Status{
		Cluster:      c.Name,
		ClusterDir:   c.Dir,
		InstallDir:   a.cfg.InstallDir,
		AgentVersion: a.cfg.Version,
	}
	for _, sh := range c.shards(a.cfg) {
		active, restarts := a.run.serviceState(ctx, sh.Service)
		st.Shards = append(st.Shards, proto.ShardStatus{
			Name: sh.Name, Service: sh.Service, Active: active, Port: sh.Port, Restarts: restarts,
		})
	}
	if v, err := os.ReadFile(filepath.Join(a.cfg.InstallDir, "version.txt")); err == nil {
		st.GameVersion = strings.TrimSpace(string(v))
	}
	if mods, err := a.modsList(ctx, c); err == nil {
		st.ModsEnabled = 0
		for _, m := range mods.Mods {
			if m.Enabled {
				st.ModsEnabled++
			}
			if m.Installed {
				st.ModsInstalled++
			}
		}
	}
	st.PlayersOnline = a.playersOnline(ctx, c)
	st.MemTotalKB, st.MemUsedKB, st.SwapTotalKB = memInfo()
	st.DiskTotalKB, st.DiskUsedKB = diskInfo(a.cfg.InstallDir)
	return st, nil
}

func (a *Agent) playersOnline(ctx context.Context, c clusterDef) bool {
	for _, sh := range c.shards(a.cfg) {
		out, _ := a.run.run(ctx, 20*time.Second, "journalctl", "-u", sh.Service, "-n", "400", "--no-pager", "-o", "cat")
		last := ""
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "Sim paused") {
				last = "paused"
			} else if strings.Contains(line, "Sim unpaused") {
				last = "unpaused"
			}
		}
		if last == "unpaused" {
			return true
		}
	}
	return false
}

func (a *Agent) logsTail(ctx context.Context, service string, lines int) (*proto.LogsReply, error) {
	if lines <= 0 || lines > 500 {
		lines = 120
	}
	unit := service
	if unit == "" {
		unit = a.cfg.Shards[0].Service
	}
	out, err := a.run.run(ctx, 30*time.Second, "journalctl", "-u", unit, "-n", fmt.Sprint(lines), "--no-pager", "-o", "short-iso")
	if err != nil {
		return nil, err
	}
	reply := &proto.LogsReply{Service: unit}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" {
			reply.Lines = append(reply.Lines, line)
		}
	}
	return reply, nil
}

// restartShards restarts the requested shards (all when empty) and waits for readiness.
func (a *Agent) restartShards(ctx context.Context, job *proto.Job, names []string) error {
	units := a.selectedShards(names)
	for _, sh := range units {
		since := time.Now().Add(-2 * time.Second)
		a.jobLog(job, "重启 "+sh.Service)
		if err := a.run.restartService(ctx, sh.Service); err != nil {
			return err
		}
		if err := a.run.waitStarted(ctx, sh.Service, since, 8*time.Minute); err != nil {
			return err
		}
		a.jobLog(job, sh.Service+" 已就绪")
	}
	return nil
}

func (a *Agent) selectedShards(names []string) []Shard {
	if len(names) == 0 {
		return a.cfg.Shards
	}
	set := map[string]bool{}
	for _, n := range names {
		set[strings.ToLower(n)] = true
	}
	var out []Shard
	for _, sh := range a.cfg.Shards {
		if set[strings.ToLower(sh.Name)] {
			out = append(out, sh)
		}
	}
	return out
}
