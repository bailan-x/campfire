// Package proto defines the JSON-lines RPC spoken between the local panel and
// the cloud-side Agent over a unix socket (reached through an SSH
// direct-streamlocal channel).
package proto

import "encoding/json"

// Request is one RPC call.
type Request struct {
	ID     int64           `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is one RPC reply.
type Response struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Error payload for failures the UI should explain to the user.
type Error struct{ Message string `json:"message"` }

// ShardStatus describes one running shard.
type ShardStatus struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	Active  bool   `json:"active"`
	Port    int    `json:"port"`
	Restarts int   `json:"restarts"`
}

// Status is the /overview payload.
type Status struct {
	Cluster       string        `json:"cluster"`
	ClusterDir    string        `json:"cluster_dir"`
	InstallDir    string        `json:"install_dir"`
	GameVersion   string        `json:"game_version"`
	Shards        []ShardStatus `json:"shards"`
	ModsEnabled   int           `json:"mods_enabled"`
	ModsInstalled int           `json:"mods_installed"`
	PlayersOnline bool          `json:"players_online"`
	MemTotalKB    int64         `json:"mem_total_kb"`
	MemUsedKB     int64         `json:"mem_used_kb"`
	SwapTotalKB   int64         `json:"swap_total_kb"`
	DiskTotalKB   int64         `json:"disk_total_kb"`
	DiskUsedKB    int64         `json:"disk_used_kb"`
	AgentVersion  string        `json:"agent_version"`
	LastError     string        `json:"last_error,omitempty"`
}

// ModOption is one entry of modinfo.lua's configuration_options.
type ModOption struct {
	Name    string       `json:"name"`
	Label   string       `json:"label"`
	Hover   string       `json:"hover"`
	Default string       `json:"default"`
	Choices []ModChoice  `json:"choices"`
}

// ModChoice is one selectable option value.
type ModChoice struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ModInfo describes one workshop mod.
type ModInfo struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	Description   string            `json:"description"`
	Author        string            `json:"author"`
	ClientOnly    bool              `json:"client_only"`
	AllClients    bool              `json:"all_clients"`
	Enabled       bool              `json:"enabled"`
	Installed     bool              `json:"installed"`
	SizeBytes     int64             `json:"size_bytes"`
	WorkshopTitle string            `json:"workshop_title"`
	PreviewURL    string            `json:"preview_url,omitempty"`
	Subscriptions int64             `json:"subscriptions"`
	NeedsUpdate   bool              `json:"needs_update"`
	Options       []ModOption       `json:"options"`
	Current       map[string]string `json:"current"`
	UpdatedAt     int64             `json:"updated_at"`
}

// ModsList is the mods page payload.
type ModsList struct {
	Mods        []ModInfo `json:"mods"`
	Collections []string  `json:"collections"`
	NeedsUpdate bool      `json:"needs_update"`
}

// WorldgenCatalog is the curated list of worldgen settings the UI offers.
type WorldgenCatalog struct {
	Groups     []WorldgenGroup `json:"groups"`
	Playstyles []Playstyle     `json:"playstyles"`
	Presets    []Preset        `json:"presets"`
}

// Playstyle is one "游戏风格" (relaxed / endless / survival / wilderness / lightsout).
type Playstyle struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Preset    string            `json:"preset"`
	Desc      string            `json:"desc"`
	Overrides map[string]string `json:"overrides"`
}

// Preset is a settings preset id that worldgenoverride.lua can reference.
// Cave reports whether the preset only makes sense for a cave shard: writing a
// forest preset into the cave shard is what turns the caves into a second
// overworld, so this flag is the single source of truth for both the agent
// validation and the UI filtering.
type Preset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Cave bool   `json:"cave,omitempty"`
}

// WorldgenGroup groups settings in the UI.
type WorldgenGroup struct {
	Name     string           `json:"name"`
	Kind     string           `json:"kind"` // settings | worldgen
	Settings []WorldgenSetting `json:"settings"`
}

// WorldgenSetting is one worldgen key.
type WorldgenSetting struct {
	Key             string      `json:"key"`
	Label           string      `json:"label"`
	Hover           string      `json:"hover"`
	Default         string      `json:"default"`
	Worlds          []string    `json:"worlds"`
	Choices         []ModChoice `json:"choices"`
	MasterControlled bool       `json:"master_controlled"`

	// 分片归类（由 agent 依据游戏 customize.lua 的 world 字段解析）：
	//   Scope  = forest | cave | both | global
	//   Phase  = generation（世界生成，生成后锁定）| rules（世界规则，运行时可改）
	//   Shards = 该设置项真正生效的分片（global 只归地上/森林主控）
	Scope  string   `json:"scope"`
	Phase  string   `json:"phase"`
	Shards []string `json:"shards"`
}

// WorldgenState is the worldgen page payload.
type WorldgenState struct {
	Shard           string            `json:"shard"`
	Cluster         string            `json:"cluster"`
	Preset          string            `json:"preset"`
	PresetName      string            `json:"preset_name"`
	OverrideEnabled bool              `json:"override_enabled"`
	Overrides       map[string]string `json:"overrides"`
	HasOverrideFile bool              `json:"has_override_file"`
	WorldSeed       string            `json:"world_seed,omitempty"`
	Catalog         WorldgenCatalog   `json:"catalog"`

	// 世界类型校验：分片必须用对应类型的预设，否则游戏会回落到默认关卡
	// （洞穴分片缺洞穴预设 = 生成一片新的地上世界，即 2026-11-02 那次故障）。
	WorldType      string `json:"world_type"`                // forest | cave
	ExpectedPreset string `json:"expected_preset"`           // 该分片应有的预设
	PresetValid    bool   `json:"preset_valid"`              // 当前配置能否生成正确类型的世界
	PresetHint     string `json:"preset_hint,omitempty"`     // 不合法时给用户看的说明
	SaveWorldType  string `json:"save_world_type,omitempty"` // 磁盘上存档实际类型（forest/cave）
	SaveTypeMatch  bool   `json:"save_type_match"`           // 存档类型是否与分片匹配
	CavesEnabled   bool   `json:"caves_enabled"`             // 所属世界是否开了洞穴分片
}

// Job describes a long-running agent task.
type Job struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	State   string   `json:"state"` // running | done | failed
	Log     []string `json:"log"`
	Error   string   `json:"error,omitempty"`
	Started int64    `json:"started"`
	Ended   int64    `json:"ended,omitempty"`
}

// LogsReply is a tail of one systemd unit.
type LogsReply struct {
	Service string   `json:"service"`
	Lines   []string `json:"lines"`
}

// ClusterBrief describes one world (cluster) on the node.
type ClusterBrief struct {
	Name         string        `json:"name"`
	Title        string        `json:"title"`
	Dir          string        `json:"dir"`
	Active       bool          `json:"active"`
	Running      bool          `json:"running"`
	GameMode     string        `json:"game_mode"`
	GameModeName string        `json:"game_mode_name"`
	Description  string        `json:"description"`
	Password     string        `json:"password"`
	MaxPlayers   int           `json:"max_players"`
	PvP          bool          `json:"pvp"`
	Caves        bool          `json:"caves"`
	MasterPort   int           `json:"master_port"`
	CavesPort    int           `json:"caves_port"`
	SteamPort    int           `json:"steam_port"`
	AuthPort     int           `json:"auth_port"`
	Shards       []ShardStatus `json:"shards"`
	CreatedAt    int64         `json:"created_at,omitempty"`
}

// ClustersReply is the worlds page payload.
type ClustersReply struct {
	Active   string         `json:"active"`
	Clusters []ClusterBrief `json:"clusters"`
}

// CreateWorldParams is the world-creation wizard payload.
type CreateWorldParams struct {
	Name            string            `json:"name"`
	GameMode        string            `json:"game_mode"`
	Description     string            `json:"description"`
	Password        string            `json:"password"`
	MaxPlayers      int               `json:"max_players"`
	PvP             bool              `json:"pvp"`
	Caves           bool              `json:"caves"`
	MasterPreset    string            `json:"master_preset"`
	CavesPreset     string            `json:"caves_preset"`
	MasterOverrides map[string]string `json:"master_overrides"`
	CavesOverrides  map[string]string `json:"caves_overrides"`
	Start           bool              `json:"start"`
	CopyMods        bool              `json:"copy_mods"`
}

// ShardModState is one shard's modoverrides entry for a single mod.
type ShardModState struct {
	Shard   string            `json:"shard"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// ModDetail is the mod info page payload (workshop metadata + local state).
type ModDetail struct {
	ModInfo
	PreviewURL        string          `json:"preview_url"`
	Creator           string          `json:"creator"`
	Tags              []string        `json:"tags"`
	Views             int64           `json:"views"`
	CreatedAt         int64           `json:"created_at"`
	WorkshopUpdatedAt int64           `json:"workshop_updated_at"`
	ShardStates       []ShardModState `json:"shard_states"`
	CatalogURL        string          `json:"catalog_url"`
}

// ImageReply is a proxied workshop image (base64 so it survives JSON lines).
type ImageReply struct {
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

// ConsoleExecParams runs one DST console command on a shard. DST has no RCON:
// the command is written to the shard's stdin pipe (see campfire-shard-run).
type ConsoleExecParams struct {
	Cluster string `json:"cluster"`
	Shard   string `json:"shard"`
	Command string `json:"command"`
}

// ConsoleExecResult is the captured output of one console command.
type ConsoleExecResult struct {
	Shard   string   `json:"shard"`
	Command string   `json:"command"`
	Lines   []string `json:"lines"`
	Message string   `json:"message,omitempty"`
}
// Snapshot is one historical save directory (save-<stamp>) of a shard.
type Snapshot struct {
	Shard     string `json:"shard"`
	Name      string `json:"name"`
	Modified  int64  `json:"modified"`
	SizeBytes int64  `json:"size_bytes"`
	SessionID string `json:"session_id,omitempty"`
	WorldType string `json:"world_type,omitempty"`
	Seed      string `json:"seed,omitempty"`
}

// SnapshotList is the response of worldgen.snapshots.
type SnapshotList struct {
	Cluster   string     `json:"cluster"`
	Keep      int        `json:"keep"`
	Snapshots []Snapshot `json:"snapshots"`
}