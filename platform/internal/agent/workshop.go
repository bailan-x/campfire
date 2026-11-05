package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const steamAPI = "https://api.steampowered.com"

type workshopPreview struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	UpdatedAt     int64    `json:"updated_at"`
	SizeBytes     int64    `json:"size_bytes"`
	Subscriptions int64    `json:"subscriptions"`
	PreviewURL    string   `json:"preview_url,omitempty"`
	Creator       string   `json:"creator,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Views         int64    `json:"views"`
	CreatedAt     int64    `json:"created_at"`
	Installed     bool     `json:"installed"`
	Collection    bool     `json:"collection"`
	Children      []string `json:"children,omitempty"`
}

// workshopLookup resolves whatever the user pasted — a workshop id, a
// steamcommunity item URL or a collection URL — into one or more previews.
// Steam's search API needs an API key, so pasting a link/id is the supported
// way to "browse" the workshop from the panel.
func (a *Agent) workshopLookup(ctx context.Context, input string) (*workshopLookupResult, error) {
	id := firstWorkshopID(input)
	if id == "" {
		return nil, fmt.Errorf("没找到创意工坊 ID：请粘贴创意工坊链接（...?id=123456789）或纯数字 ID")
	}
	res := &workshopLookupResult{Input: input, ID: id, Kind: "mod"}
	metas, err := getPublishedFileDetails(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	if len(metas) == 1 && metas[0].Title != "" && !strings.HasPrefix(metas[0].Title, "（查询失败") {
		metas[0].Installed = a.modInstalled(id)
		res.Items = metas
		return res, nil
	}
	// Not a plain item: try to treat it as a collection.
	children, err := getCollectionDetails(ctx, id)
	if err != nil {
		return res, nil
	}
	res.Kind = "collection"
	prev := workshopPreview{ID: id, Collection: true, Children: children, Installed: a.modInstalled(id)}
	if m, err := getPublishedFileDetails(ctx, []string{id}); err == nil && len(m) > 0 {
		prev.Title = m[0].Title
		prev.Description = m[0].Description
	}
	res.Items = append(res.Items, prev)
	childMetas, err := getPublishedFileDetails(ctx, children)
	if err != nil {
		return res, nil
	}
	for _, m := range childMetas {
		m.Installed = a.modInstalled(m.ID)
		res.Items = append(res.Items, m)
	}
	return res, nil
}

// workshopLookupResult is the payload of the "paste a link" search box.
type workshopLookupResult struct {
	Input string            `json:"input"`
	ID    string            `json:"id"`
	Kind  string            `json:"kind"`
	Items []workshopPreview `json:"items"`
}

// firstWorkshopID pulls the first 5+ digit number out of a pasted link or id.
func firstWorkshopID(s string) string {
	best := ""
	cur := ""
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur += string(r)
			continue
		}
		if len(cur) >= 5 && len(cur) > len(best) {
			best = cur
		}
		cur = ""
	}
	if len(cur) >= 5 && len(cur) > len(best) {
		best = cur
	}
	return best
}

// workshopResolve turns workshop ids / collection ids into previews so the user
// can confirm what they are about to download.
func (a *Agent) workshopResolve(ctx context.Context, ids, collections []string) ([]workshopPreview, error) {
	var out []workshopPreview
	// Expand collections first.
	for _, cid := range collections {
		children, err := getCollectionDetails(ctx, cid)
		if err != nil {
			return nil, err
		}
		prev := workshopPreview{ID: cid, Collection: true, Children: children}
		if meta, err := getPublishedFileDetails(ctx, []string{cid}); err == nil && len(meta) > 0 {
			prev.Title = meta[0].Title
			prev.Description = meta[0].Description
		}
		out = append(out, prev)
		for _, child := range children {
			ids = append(ids, child)
		}
	}
	// De-duplicate ids.
	seen := map[string]bool{}
	var uniq []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || !isDigits(id) || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	metas, err := getPublishedFileDetails(ctx, uniq)
	if err != nil {
		return nil, err
	}
	for _, m := range metas {
		m.Installed = a.modInstalled(m.ID)
		out = append(out, m)
	}
	return out, nil
}

func (a *Agent) modInstalled(id string) bool {
	if entries, err := a.readModsList(); err == nil {
		for _, e := range entries {
			if e.Kind == "mod" && e.ID == id {
				return true
			}
		}
	}
	if c, err := a.activeCluster(); err == nil {
		for _, sh := range c.shards(a.cfg) {
			if _, err := os.Stat(a.ugcDir(c, sh.Name, id)); err == nil {
				return true
			}
		}
	}
	if _, err := os.Stat(filepath.Join(a.cachedModDir(id), "modinfo.lua")); err == nil {
		return true
	}
	return false
}

func getCollectionDetails(ctx context.Context, collectionID string) ([]string, error) {
	form := url.Values{}
	form.Set("collectioncount", "1")
	form.Set("publishedfileids[0]", collectionID)
	body, err := steamPost(ctx, "/ISteamRemoteStorage/GetCollectionDetails/v1/", form)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Response struct {
			CollectionDetails []struct {
				Result   int `json:"result"`
				Children []struct {
					PublishedFileID string `json:"publishedfileid"`
				} `json:"children"`
			} `json:"collectiondetails"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("解析合集信息失败: %w", err)
	}
	if len(resp.Response.CollectionDetails) == 0 {
		return nil, fmt.Errorf("找不到合集 %s", collectionID)
	}
	cd := resp.Response.CollectionDetails[0]
	if cd.Result != 1 {
		return nil, fmt.Errorf("合集 %s 查询失败（result=%d），确认它是创意工坊合集 ID", collectionID, cd.Result)
	}
	var out []string
	for _, c := range cd.Children {
		out = append(out, c.PublishedFileID)
	}
	return out, nil
}

// flexInt64 accepts both JSON numbers and numeric strings: the Steam web API
// returns large values (file_size, time_updated, subscriptions) as strings.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		*f = flexInt64(v)
		return nil
	}
	var fl float64
	if ferr := json.Unmarshal(b, &fl); ferr == nil {
		*f = flexInt64(int64(fl))
		return nil
	}
	return err
}

func getPublishedFileDetails(ctx context.Context, ids []string) ([]workshopPreview, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	form := url.Values{}
	form.Set("itemcount", strconv.Itoa(len(ids)))
	for i, id := range ids {
		form.Set(fmt.Sprintf("publishedfileids[%d]", i), id)
	}
	body, err := steamPost(ctx, "/ISteamRemoteStorage/GetPublishedFileDetails/v1/", form)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Response struct {
			PublishedFileDetails []struct {
				PublishedFileID string    `json:"publishedfileid"`
				Result          int       `json:"result"`
				Title           string    `json:"title"`
				Description     string    `json:"description"`
				TimeUpdated     flexInt64 `json:"time_updated"`
				TimeCreated     flexInt64 `json:"time_created"`
				FileSize        flexInt64 `json:"file_size"`
				Subscriptions   flexInt64 `json:"subscriptions"`
				Views           flexInt64 `json:"views"`
				PreviewURL      string    `json:"preview_url"`
				Creator         string    `json:"creator"`
				Tags            []struct {
					Tag string `json:"tag"`
				} `json:"tags"`
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("解析工坊信息失败: %w", err)
	}
	var out []workshopPreview
	for _, d := range resp.Response.PublishedFileDetails {
		if d.Result != 1 {
			out = append(out, workshopPreview{ID: d.PublishedFileID, Title: "（查询失败，ID 可能不存在）"})
			continue
		}
		var tags []string
		for _, tg := range d.Tags {
			if strings.TrimSpace(tg.Tag) != "" {
				tags = append(tags, tg.Tag)
			}
		}
		out = append(out, workshopPreview{
			ID:            d.PublishedFileID,
			Title:         d.Title,
			Description:   stripBBCode(d.Description),
			UpdatedAt:     int64(d.TimeUpdated),
			SizeBytes:     int64(d.FileSize),
			Subscriptions: int64(d.Subscriptions),
			PreviewURL:    d.PreviewURL,
			Creator:       d.Creator,
			Tags:          tags,
			Views:         int64(d.Views),
			CreatedAt:     int64(d.TimeCreated),
		})
	}
	return out, nil
}

// ---- 工坊元数据缓存 ----
//
// 国内网络连 api.steampowered.com 会偶发超时（一次查询最长要 30 秒以上），
// 而模组列表页每次打开都要补标题。缓存 6 小时，过期才重新查，查失败就退回
// 旧缓存，保证 /api/mods 不会因为 Steam 抽风而卡住。
const workshopCacheTTL = 6 * time.Hour

// workshopCacheVersion must be bumped whenever the stored previews would change
// shape (e.g. the BBCode stripper starts removing something extra), otherwise a
// stale entry keeps serving the old text.
const workshopCacheVersion = 3

type workshopCacheEntry struct {
	Preview   workshopPreview `json:"preview"`
	FetchedAt int64           `json:"fetched_at"`
}

type workshopCacheFile struct {
	Version int                           `json:"version"`
	Entries map[string]workshopCacheEntry `json:"entries"`
}

func (a *Agent) workshopCachePath() string {
	return filepath.Join(a.cfg.Home, ".campfire", "workshop-cache.json")
}

func (a *Agent) loadWorkshopCache() map[string]workshopCacheEntry {
	out := map[string]workshopCacheEntry{}
	data, err := os.ReadFile(a.workshopCachePath())
	if err != nil {
		return out
	}
	var file workshopCacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		return out
	}
	if file.Version != workshopCacheVersion || file.Entries == nil {
		return out
	}
	return file.Entries
}

func (a *Agent) saveWorkshopCache(c map[string]workshopCacheEntry) {
	file := workshopCacheFile{Version: workshopCacheVersion, Entries: c}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return
	}
	path := a.workshopCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// getPublishedFileDetailsCached returns metadata for ids, preferring the disk
// cache; only the stale/unknown ids hit the network.
func (a *Agent) getPublishedFileDetailsCached(ctx context.Context, ids []string) ([]workshopPreview, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cache := a.loadWorkshopCache()
	now := time.Now().Unix()
	ttl := int64(workshopCacheTTL.Seconds())
	var missing []string
	out := make([]workshopPreview, 0, len(ids))
	for _, id := range ids {
		if e, ok := cache[id]; ok && now-e.FetchedAt < ttl {
			p := e.Preview
			p.ID = id
			out = append(out, p)
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return out, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	fresh, err := getPublishedFileDetails(cctx, missing)
	if err != nil {
		for _, id := range missing {
			if e, ok := cache[id]; ok {
				p := e.Preview
				p.ID = id
				out = append(out, p)
			}
		}
		return out, err
	}
	for _, p := range fresh {
		cache[p.ID] = workshopCacheEntry{Preview: p, FetchedAt: now}
		out = append(out, p)
	}
	a.saveWorkshopCache(cache)
	return out, nil
}

func steamPost(ctx context.Context, path string, form url.Values) ([]byte, error) {
	// Steam 的接口在国内网络偶尔会掉连接（unexpected EOF），重试几次再报错。
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		body, err := steamPostOnce(ctx, path, form)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if respErr, ok := err.(*steamHTTPError); ok && respErr.Code < 500 {
			return nil, err
		}
	}
	return nil, lastErr
}

type steamHTTPError struct {
	Code int
}

func (e *steamHTTPError) Error() string { return fmt.Sprintf("Steam API 返回 HTTP %d", e.Code) }

func steamPostOnce(ctx context.Context, path string, form url.Values) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, steamAPI+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("访问 Steam API 失败（服务器可能无法访问 api.steampowered.com）: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &steamHTTPError{Code: resp.StatusCode}
	}
	return body, nil
}

var (
	imgBlockRe = regexp.MustCompile(`(?is)\[img\][^\[]*\[/img\]`)
	imgTagRe   = regexp.MustCompile(`(?is)\[img=[^\]]*\][^\[]*\[/img\]`)
	urlTagRe   = regexp.MustCompile(`(?is)\[url=[^\]]*\]`)
	listTagRe  = regexp.MustCompile(`(?is)\[/?(list|\*|hr|quote|code|table|tr|td|th|b|i|u|h[1-6])\]`)
	urlOnlyRe  = regexp.MustCompile(`(?im)^\s*https?://\S+\s*$`)
	multiNLRe  = regexp.MustCompile(`\n{3,}`)
)

// stripBBCode turns a workshop description into plain text: images are dropped
// (otherwise the raw steamuserimages URL leaks into the mod list), links keep
// their label, and tag-only noise lines disappear.
func stripBBCode(s string) string {
	out := s
	out = imgBlockRe.ReplaceAllString(out, "")
	out = imgTagRe.ReplaceAllString(out, "")
	out = urlTagRe.ReplaceAllString(out, "")
	out = listTagRe.ReplaceAllString(out, "\n")
	out = strings.ReplaceAll(out, "[/url]", "")
	out = strings.ReplaceAll(out, "[/img]", "")
	out = urlOnlyRe.ReplaceAllString(out, "")
	out = multiNLRe.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}
