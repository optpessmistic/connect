// Package httpapi exposes a local, same-origin UI API shared by Wails and Vite.
// It is a panel/configuration bridge, not an inference proxy.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"surplustoken-connect/internal/configedit"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/gateway"
)

type localSession struct {
	remote  *gateway.Session
	created time.Time
	ready   bool
}
type API struct {
	Store        CredentialStore
	authMu       sync.Mutex // Serializes login, OTP, resume, logout, and revocation.
	storeMu      sync.Mutex
	savedID      string
	storeVersion uint64
	clearPending bool
	Fixture      bool
	Gateway      *gateway.Client
	Engine       *configuration.Engine
	mu           sync.Mutex
	sessions     map[string]*localSession
}

func New(g *gateway.Client, e *configuration.Engine) *API {
	return &API{Gateway: g, Engine: e, sessions: map[string]*localSession{}}
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) { respond(w, 400, map[string]string{"error": e.Error()}) }
func (a *API) session(id string) *localSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[id]
	if s != nil && time.Since(s.created) > 8*time.Hour {
		delete(a.sessions, id)
		a.Engine.Forget(id)
		return nil
	}
	return s
}
func (a *API) finishLogin(ctx context.Context, id string, s *localSession) (any, error) {
	u, e := s.remote.Profile(ctx)
	if e != nil {
		return nil, e
	}
	g, e := s.remote.Groups(ctx)
	if e != nil {
		return nil, e
	}
	k, e := s.remote.Keys(ctx)
	if e != nil {
		return nil, e
	}
	pub := []gateway.PublicKey{}
	for _, v := range k {
		pub = append(pub, v.Public())
	}
	return map[string]any{"session": id, "user": u, "groups": g, "keys": pub, "clients": a.Engine.Detect(), "fixture": a.Fixture}, nil
}
func (a *API) key(ctx context.Context, s *localSession, id int64) (gateway.Key, gateway.Group, error) {
	k, e := s.remote.Key(ctx, id)
	if e != nil {
		return k, gateway.Group{}, e
	}
	if !k.Usable() {
		return k, gateway.Group{}, errors.New("所选 Key 已停用、过期或不可读取")
	}
	groups, e := s.remote.Groups(ctx)
	if e != nil {
		return k, gateway.Group{}, e
	}
	for _, g := range groups {
		if g.ID == k.GroupID {
			return k, g, nil
		}
	}
	return k, gateway.Group{}, errors.New("Key 分组不在当前可用范围")
}
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		respond(w, 405, map[string]string{"error": "此接口只接受 POST"})
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" && origin != "null" && origin != "wails://localhost" && origin != "http://wails.localhost" && origin != "https://wails.localhost" && origin != "http://127.0.0.1:5173" {
		respond(w, 403, map[string]string{"error": "来源不允许"})
		return
	}
	if r.Header.Get("X-Connect-Request") != "1" {
		respond(w, 403, map[string]string{"error": "请求缺少客户端标识"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Code     string `json:"code"`
		GroupID  int64  `json:"group_id"`
		KeyID    int64  `json:"key_id"`
		Client   string `json:"client"`
		Model    string `json:"model"`
		PlanID   string `json:"plan_id"`
		RecordID string `json:"record_id"`
		Mode     string `json:"mode"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if e := dec.Decode(&in); e != nil {
		fail(w, errors.New("请求格式无效"))
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/connect/")
	id := r.Header.Get("X-Connect-Session")
	authLocked := action == "login" || action == "login-otp" || action == "resume" || action == "logout"
	if authLocked {
		a.authMu.Lock()
		defer a.authMu.Unlock()
	}
	if action == "resume" {
		a.resume(w, r)
		return
	}
	if action == "login" {
		if len(in.Email) > 320 || len(in.Password) > 1024 || in.Email == "" || in.Password == "" {
			fail(w, errors.New("请输入账号和密码"))
			return
		}
		s, otp, e := a.Gateway.Login(r.Context(), in.Email, in.Password)
		in.Password = ""
		if e != nil {
			fail(w, e)
			return
		}
		id = configuration.ID()
		local := &localSession{remote: s, created: time.Now(), ready: !otp}
		a.mu.Lock()
		for sid, v := range a.sessions {
			if time.Since(v.created) > 8*time.Hour {
				delete(a.sessions, sid)
			}
		}
		if len(a.sessions) >= 32 {
			a.mu.Unlock()
			fail(w, errors.New("本地登录会话过多，请重新打开 Connect"))
			return
		}
		a.sessions[id] = local
		a.mu.Unlock()
		if otp {
			respond(w, 200, map[string]any{"session": id, "requires_2fa": true})
			return
		}
		v, e := a.finishLogin(r.Context(), id, local)
		if e != nil {
			a.mu.Lock()
			delete(a.sessions, id)
			a.mu.Unlock()
			a.failSession(w, id, local, e, authLocked)
			return
		}
		a.remember(id, local, v)
		respond(w, 200, v)
		return
	}
	if action == "logout" {
		// The local session may have expired while its refresh token remains in
		// the OS store. Only the matching nonempty ID can remove saved login.
		if id == "" {
			respond(w, 401, map[string]string{"error": "请先登录"})
			return
		}
		a.mu.Lock()
		s := a.sessions[id]
		delete(a.sessions, id)
		a.mu.Unlock()
		a.Engine.Forget(id)
		cleanupErr := a.forgetSaved(id)
		if s != nil {
			_ = s.remote.Logout(r.Context())
		}
		if cleanupErr != nil {
			fail(w, cleanupErr)
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	s := a.session(id)
	if s == nil {
		respond(w, 401, map[string]string{"error": "请先登录"})
		return
	}
	if action == "login-otp" {
		if len(in.Code) != 6 {
			fail(w, errors.New("请输入六位验证码"))
			return
		}
		if e := s.remote.CompleteOTP(r.Context(), in.Code); e != nil {
			fail(w, e)
			return
		}
		a.mu.Lock()
		s.ready = true
		a.mu.Unlock()
		v, e := a.finishLogin(r.Context(), id, s)
		if e != nil {
			a.failSession(w, id, s, e, authLocked)
			return
		}
		a.remember(id, s, v)
		respond(w, 200, v)
		return
	}
	a.mu.Lock()
	ready := s.ready
	a.mu.Unlock()
	if !ready {
		respond(w, 401, map[string]string{"error": "请完成二次验证"})
		return
	}
	switch action {
	case "session":
		v, e := a.finishLogin(r.Context(), id, s)
		if e != nil {
			a.failSession(w, id, s, e, authLocked)
			return
		}
		respond(w, 200, v)
	case "create-key":
		groups, e := s.remote.Groups(r.Context())
		if e != nil {
			a.failSession(w, id, s, e, authLocked)
			return
		}
		allowed := false
		for _, g := range groups {
			if g.ID == in.GroupID && g.Status == "active" {
				allowed = true
			}
		}
		if !allowed {
			fail(w, errors.New("请选择可用分组"))
			return
		}
		k, e := s.remote.CreateKey(r.Context(), in.GroupID)
		if e != nil {
			a.failSession(w, id, s, e, authLocked)
			return
		}
		respond(w, 200, k.Public())
	case "models":
		k, g, e := a.key(r.Context(), s, in.KeyID)
		if e != nil {
			a.failSession(w, id, s, e, authLocked)
			return
		}
		models, e := s.remote.Models(r.Context(), k)
		if e != nil {
			a.failSession(w, id, s, e, authLocked)
			return
		}
		respond(w, 200, map[string]any{"models": models, "group": g, "messages": g.Messages()})
	case "detect":
		respond(w, 200, a.Engine.Detect())
	case "plan":
		if in.Client != "opencode" && (len(in.Model) > 200 || !regexp.MustCompile(`^[a-zA-Z0-9_.:/-]+$`).MatchString(in.Model)) {
			fail(w, errors.New("模型标识格式不支持"))
			return
		}
		if in.Mode != "permanent" {
			fail(w, errors.New("本轮先开放永久配置，临时终端启动尚未接入"))
			return
		}
		if in.Client != "claude" && in.Client != "opencode" {
			fail(w, errors.New("此客户端尚未开放写入"))
			return
		}
		for _, c := range a.Engine.Detect() {
			if c.ID == in.Client && !c.Supported {
				fail(w, errors.New(c.Reason))
				return
			}
		}
		k, g, e := a.key(r.Context(), s, in.KeyID)
		if e != nil {
			a.failSession(w, id, s, e, authLocked)
			return
		}
		models, e := s.remote.Models(r.Context(), k)
		if e != nil {
			a.failSession(w, id, s, e, authLocked)
			return
		}
		found := false
		for _, m := range models {
			if m == in.Model {
				found = true
			}
		}
		if in.Client != "opencode" && !found {
			fail(w, errors.New("模型已不在当前 Key 的可用列表，请刷新"))
			return
		}
		patches := []configedit.KV{}
		if in.Client == "claude" {
			if !g.Messages() {
				fail(w, errors.New("此分组未声明 Messages 兼容，不能配置 Claude Code"))
				return
			}
			if raw, err := os.ReadFile(a.Engine.Paths[in.Client]); err == nil {
				current := gjson.ParseBytes(jsonc.ToJSON(raw))
				for _, flag := range []string{"env.CLAUDE_CODE_USE_BEDROCK", "env.CLAUDE_CODE_USE_VERTEX", "env.CLAUDE_CODE_USE_FOUNDRY"} {
					if v := current.Get(flag).String(); v == "1" || v == "true" {
						fail(w, errors.New("已有云平台路由配置，请先在客户端中关闭或选择独立配置目录"))
						return
					}
				}
			}
			patches = []configedit.KV{{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.Origin}, {Path: "env.ANTHROPIC_AUTH_TOKEN", Value: k.Value}, {Path: "env.ANTHROPIC_API_KEY", Value: ""}, {Path: "env.ANTHROPIC_MODEL", Value: in.Model}, {Path: "env.ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: in.Model}, {Path: "env.ANTHROPIC_DEFAULT_SONNET_MODEL", Value: in.Model}, {Path: "env.ANTHROPIC_DEFAULT_OPUS_MODEL", Value: in.Model}, {Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: in.Model}}
		} else {
			if g.Platform != "openai" && g.Platform != "anthropic" && g.Platform != "deepseek" && g.Platform != "kimi" && g.Platform != "composite" {
				fail(w, errors.New("该分组平台尚未适配 OpenCode"))
				return
			}
			patches = openCodeV2Patches(k, g, models)
			if len(patches) == 0 {
				fail(w, errors.New("混合分组需要明确协议，目前请使用单协议分组"))
				return
			}

		}
		if in.Client == "opencode" {
			if raw, err := os.ReadFile(a.Engine.Paths[in.Client]); err == nil {
				current := gjson.ParseBytes(jsonc.ToJSON(raw))
				for _, disabled := range current.Get("disabled_providers").Array() {
					if disabled.String() == "surplustoken" {
						fail(w, errors.New("SurplusToken 在 disabled_providers 中，请先解除禁用"))
						return
					}
				}
				if enabled := current.Get("enabled_providers"); enabled.IsArray() && len(enabled.Array()) > 0 {
					values := []string{}
					found := false
					for _, item := range enabled.Array() {
						values = append(values, item.String())
						if item.String() == "surplustoken" {
							found = true
						}
					}
					if !found {
						values = append(values, "surplustoken")
						patches = append(patches, configedit.KV{Path: "enabled_providers", Value: values})
					}
				}
			}
		}
		var plan configuration.Plan
		if in.Client == "opencode" {
			plan, e = a.Engine.PlanManaged(id, in.Client, patches, "providers.surplustoken")
		} else {
			plan, e = a.Engine.Plan(id, in.Client, patches)
		}
		if e != nil {
			fail(w, e)
			return
		}
		if in.Client == "opencode" {
			template := map[string]string{"openai": "OpenAI · Responses API", "anthropic": "Anthropic · Messages API", "deepseek": "DeepSeek · Chat API", "kimi": "Kimi · Chat API"}[g.Platform]
			respond(w, 200, struct {
				configuration.Plan
				Template   string `json:"template"`
				ModelCount int    `json:"model_count"`
			}{plan, template, len(models)})
		} else {
			respond(w, 200, plan)
		}
	case "apply":
		v, e := a.Engine.Apply(id, in.PlanID)
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, v)
	case "history":
		v, e := a.Engine.History()
		if e != nil {
			fail(w, e)
			return
		}
		sort.Slice(v, func(i, j int) bool { return v[i].At.After(v[j].At) })
		respond(w, 200, v)
	case "restore":
		v, e := a.Engine.Restore(id, in.RecordID)
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, v)
	default:
		respond(w, 404, map[string]string{"error": "接口不存在"})
	}
}
func escape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(s, ".", "\\.")
}
