package panel

import (
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"campfire/internal/proto"
)

//go:embed web
var webFS embed.FS

// Server is the local web UI.
type Server struct {
	rpc         *RPC
	addr        string
	publicHosts []string
	authUser    string
	authPass    string
	readOnly    bool

	mu    sync.Mutex
	fails map[string]*loginFail
}

// loginFail tracks failed public logins per client so a tunnel cannot be
// brute-forced cheaply.
type loginFail struct {
	count int
	until time.Time
}

const (
	maxLoginFails = 5
	loginLockFor  = 10 * time.Minute
)

// NewServer creates the local UI. publicHosts lists extra Host values that are
// accepted besides loopback (needed when the panel is reached through a tunnel;
// entries may use a "*.example.com" wildcard). Any non-loopback request must
// pass HTTP Basic auth when authPass is set; readOnly blocks writes from those
// non-loopback requests.
func NewServer(rpc *RPC, addr string, publicHosts []string, authUser, authPass string, readOnly bool) *Server {
	return &Server{
		rpc:         rpc,
		addr:        addr,
		publicHosts: publicHosts,
		authUser:    authUser,
		authPass:    authPass,
		readOnly:    readOnly,
		fails:       map[string]*loginFail{},
	}
}

// ListenAndServe binds the loopback interface only (single-user, local-only v1).
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	s.routes(mux)

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(web)))

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	handler := s.accessGuard(hostGuard(mux, s.publicHosts))
	log.Printf("面板已启动: http://%s", s.addr)
	if len(s.publicHosts) > 0 {
		if s.authPass != "" {
			log.Printf("公网访问已启用登录：用户名 %s（本机 127.0.0.1 免登录）", s.authUser)
		} else {
			log.Printf("⚠️ 已允许非本机 Host 访问（%s）且未设置口令：任何拿到该地址的人都能操作面板，仅限临时演示", strings.Join(s.publicHosts, ", "))
		}
		if s.readOnly {
			log.Printf("只读演示模式已开启：非本机访问只能查看，写操作会被拒绝")
		}
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	return srv.Serve(ln)
}

// accessGuard enforces login and read-only mode for non-loopback requests so a
// shared tunnel link cannot be used to control the server. Loopback (the owner
// at the keyboard) stays passwordless.
func (s *Server) accessGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackHost(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		if s.authPass != "" {
			if locked, wait := s.loginLocked(ip); locked {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				http.Error(w, "登录失败次数过多，请稍后再试", http.StatusTooManyRequests)
				return
			}
			ok, missing := s.basicAuth(r)
			if !ok {
				// A browser's first probe carries no credentials at all; that is
				// not a wrong guess, so it must not burn a lockout slot.
				if !missing {
					s.recordFail(ip)
				}
				w.Header().Set("WWW-Authenticate", `Basic realm="Campfire", charset="UTF-8"`)
				if missing {
					http.Error(w, "需要登录：请输入面板用户名与口令", http.StatusUnauthorized)
				} else {
					http.Error(w, "用户名或口令不正确", http.StatusUnauthorized)
				}
				return
			}
			s.clearFail(ip)
		}
		if s.readOnly && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"只读演示模式：公网访问不能执行写操作（请在运行面板的电脑上操作）"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost reports whether the Host header names the local machine.
func isLoopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// clientIP returns the caller address. Behind the SSH tunnel / Caddy front the
// TCP peer is always loopback, so a trusted proxy header is used instead; that
// keeps the brute-force lockout per real visitor rather than one global bucket.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "127.0.0.1" || host == "::1" {
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
			return v
		}
		if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
			if i := strings.IndexByte(v, ','); i > 0 {
				return strings.TrimSpace(v[:i])
			}
			return v
		}
	}
	return host
}

func (s *Server) loginLocked(ip string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[ip]
	if f == nil || f.until.IsZero() || time.Now().After(f.until) {
		return false, 0
	}
	return true, time.Until(f.until)
}

func (s *Server) recordFail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[ip]
	if f == nil {
		f = &loginFail{}
		s.fails[ip] = f
	}
	f.count++
	if f.count >= maxLoginFails {
		f.until = time.Now().Add(loginLockFor)
		f.count = 0
	}
}

// clearFail forgets earlier failures after a successful login.
func (s *Server) clearFail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fails, ip)
}

// basicAuth validates the Authorization header in constant time. missing
// reports that no credentials were supplied at all (a normal first hit from a
// browser, which then retries with the dialog).
func (s *Server) basicAuth(r *http.Request) (ok, missing bool) {
	const prefix = "Basic "
	h := r.Header.Get("Authorization")
	if h == "" {
		return false, true
	}
	if !strings.HasPrefix(h, prefix) {
		return false, false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		return false, false
	}
	user, pass, ok2 := strings.Cut(string(raw), ":")
	if !ok2 {
		return false, false
	}
	uOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.authUser)) == 1
	pOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.authPass)) == 1
	return uOK && pOK, false
}

// hostGuard rejects requests whose Host header is not loopback (DNS rebinding
// protection for a local-only panel) unless the host is explicitly allowlisted.
func hostGuard(next http.Handler, publicHosts []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "127.0.0.1" || host == "localhost" || host == "::1" || hostAllowed(host, publicHosts) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden host", http.StatusForbidden)
	})
}

// hostAllowed reports whether host matches one of the allowlist patterns.
func hostAllowed(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == host {
			return true
		}
		if strings.HasPrefix(p, "*.") && strings.HasSuffix(host, p[1:]) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *Server) routes(mux *http.ServeMux) {
	// 只读接口：查询参数 + 可选 JSON body 都会转发给 Agent
	s.get(mux, "/api/health", "ping")
	s.get(mux, "/api/overview", "status")
	s.get(mux, "/api/clusters", "clusters.list")
	s.get(mux, "/api/mods", "mods.list")
	s.get(mux, "/api/worldgen", "worldgen.get")
	s.get(mux, "/api/logs", "logs.tail")

	// 长任务接口：返回 Job，前端轮询 /api/jobs/<id>
	s.job(mux, "/api/clusters/create", "clusters.create")
	s.job(mux, "/api/clusters/start", "clusters.start")
	s.job(mux, "/api/clusters/stop", "clusters.stop")
	s.job(mux, "/api/clusters/remove", "clusters.remove")
	s.job(mux, "/api/mods/update", "mods.update")
	s.job(mux, "/api/mods/updateOne", "mods.updateOne")
	s.job(mux, "/api/mods/enable", "mods.setEnabled")
	s.job(mux, "/api/mods/config", "mods.setConfig")
	s.job(mux, "/api/mods/install", "mods.install")
	s.job(mux, "/api/mods/remove", "mods.remove")
	s.job(mux, "/api/worldgen/regenerate", "worldgen.regenerate")
	s.job(mux, "/api/worldgen/snapshot", "worldgen.snapshot")
	s.job(mux, "/api/worldgen/restore", "worldgen.restore")
	s.job(mux, "/api/service/restart", "service.restart")

	// 同步接口
	s.get(mux, "/api/worldgen/snapshots", "worldgen.snapshots")
	s.call(mux, "/api/clusters/activate", "clusters.activate")
	s.call(mux, "/api/mods/detail", "mods.detail")
	s.call(mux, "/api/worldgen/save", "worldgen.set")
	s.call(mux, "/api/console/exec", "console.exec")
	s.call(mux, "/api/workshop/lookup", "workshop.lookup")
	s.call(mux, "/api/workshop", "workshop.resolve")

	// 创意工坊图片代理（浏览器直连 steam 图片站经常超时）
	mux.HandleFunc("/api/image", func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		if url == "" {
			http.Error(w, "missing url", http.StatusBadRequest)
			return
		}
		var img proto.ImageReply
		if err := s.rpc.Call("workshop.image", map[string]string{"url": url}, &img); err != nil {
			http.Error(w, "image fetch failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", img.MIME)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(img.Data)
	})

	mux.HandleFunc("/api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
		var out proto.Job
		if err := s.rpc.Call("jobs.get", map[string]string{"id": id}, &out); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, out)
	})
}

// params merges the query string with an optional JSON body.
func (s *Server) params(r *http.Request) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for k, vs := range r.URL.Query() {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	if r.Method == http.MethodPost && r.Body != nil {
		var body map[string]interface{}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err == nil {
			for k, v := range body {
				out[k] = v
			}
		}
	}
	// 数字参数（lines）
	for _, k := range []string{"lines"} {
		if v, ok := out[k].(string); ok {
			if n, err := strconv.Atoi(v); err == nil {
				out[k] = n
			}
		}
	}
	return out, nil
}

func (s *Server) get(mux *http.ServeMux, path, op string) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		params, err := s.params(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		var out interface{}
		if err := s.rpc.Call(op, params, &out); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, out)
	})
}

func (s *Server) call(mux *http.ServeMux, path, op string) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		params, err := s.params(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		var out interface{}
		if err := s.rpc.Call(op, params, &out); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, out)
	})
}

func (s *Server) job(mux *http.ServeMux, path, op string) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		params, err := s.params(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		var job proto.Job
		if err := s.rpc.Call(op, params, &job); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, job)
	})
}
// callJob starts an agent job and returns it immediately so the UI can poll.
func (s *Server) callJob(w http.ResponseWriter, r *http.Request, op string, params interface{}) {
	var job proto.Job
	if err := s.rpc.Call(op, params, &job); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, job)
}

var _ = fmt.Sprintf
