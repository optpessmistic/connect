// Package gateway talks only to the configured SurplusToken origin. Panel tokens
// and inference keys never leave this Go boundary in public DTOs.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const Origin = "https://surplustoken.com"

type User struct {
	Email    string `json:"email"`
	Nickname string `json:"nickname"`
}
type Group struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	AllowMessages bool   `json:"allow_messages_dispatch"`
	Status        string `json:"status"`
}

func (g Group) Messages() bool { return g.Platform == "anthropic" || g.AllowMessages }

type Key struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	GroupID   int64   `json:"group_id"`
	Status    string  `json:"status"`
	Value     string  `json:"key"`
	ExpiresAt *string `json:"expires_at"`
}
type PublicKey struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	GroupID   int64   `json:"group_id"`
	Status    string  `json:"status"`
	Mask      string  `json:"mask"`
	ExpiresAt *string `json:"expires_at"`
}

func (k Key) Public() PublicKey {
	mask := "不可读取"
	if len(k.Value) > 8 {
		mask = "••••" + k.Value[len(k.Value)-4:]
	}
	return PublicKey{ID: k.ID, Name: k.Name, GroupID: k.GroupID, Status: k.Status, Mask: mask, ExpiresAt: k.ExpiresAt}
}
func (k Key) Usable() bool {
	if k.Status != "active" || k.Value == "" {
		return false
	}
	if k.ExpiresAt != nil {
		if t, e := time.Parse(time.RFC3339, *k.ExpiresAt); e == nil && !t.After(time.Now()) {
			return false
		}
	}
	return true
}

type Client struct {
	Base string
	HTTP *http.Client
}

func New() *Client {
	return &Client{Base: Origin, HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type RemoteError struct {
	Status    int
	Operation string
	Code      string
}

// SessionExpiredError identifies rejected panel authentication. Inference Key
// failures deliberately remain RemoteError values and never revoke panel login.
type SessionExpiredError struct{ Cause error }

func (e *SessionExpiredError) Error() string { return "登录已失效，请重新登录" }
func (e *SessionExpiredError) Unwrap() error { return e.Cause }

func rejectedAuthentication(err error) bool {
	var remote *RemoteError
	return errors.As(err, &remote) && (remote.Status == http.StatusUnauthorized || remote.Status == http.StatusForbidden)
}

func (e *RemoteError) Error() string {
	switch e.Code {
	case "INSUFFICIENT_BALANCE":
		return "账号余额不足，暂时无法读取模型列表，请在 SurplusToken 网站补充余额"
	case "QUOTA_EXCEEDED", "RATE_LIMITED":
		return "当前 Key 的配额已用尽，请检查网关额度"
	}
	switch e.Status {
	case 401:
		if e.Operation == "/v1/models" {
			return "所选 API Key 无效或已失效，请刷新或更换 Key"
		}
		return "登录已失效或凭据无效，请重新登录"
	case 403:
		return "当前账号或 Key 无权访问此功能"
	case 429:
		return "请求过于频繁，请稍后重试"
	default:
		return fmt.Sprintf("网关请求失败（%s，HTTP %d）", e.Operation, e.Status)
	}
}
func (c *Client) request(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if e != nil {
		return errors.New("网关地址错误")
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, e := c.HTTP.Do(req)
	if e != nil {
		return errors.New("无法连接 SurplusToken，请检查网络")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var detail struct {
			Code  string `json:"code"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&detail)
		if detail.Code == "" {
			detail.Code = detail.Error.Code
		}
		return &RemoteError{Status: res.StatusCode, Operation: path, Code: detail.Code}
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if e != nil {
		return errors.New("网关响应读取失败")
	}
	if strings.HasPrefix(path, "/api/") {
		var v struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &v) != nil || len(v.Data) == 0 {
			return errors.New("网关响应格式不兼容")
		}
		if v.Code != 0 {
			return errors.New("网关拒绝了操作")
		}
		raw = v.Data
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return errors.New("网关响应格式不兼容")
	}
	return nil
}

type Tokens struct {
	Access      string `json:"access_token"`
	Refresh     string `json:"refresh_token"`
	ExpiresIn   int    `json:"expires_in"`
	Requires2FA bool   `json:"requires_2fa"`
	TempToken   string `json:"temp_token"`
	User        struct {
		Email    string `json:"email"`
		Username string `json:"username"`
	} `json:"user"`
}
type Session struct {
	mu                         sync.Mutex
	client                     *Client
	access, refresh, challenge string
	expires                    time.Time
	onRefresh                  func(string) error
	User                       User
}

func (c *Client) Login(ctx context.Context, email, password string) (*Session, bool, error) {
	var t Tokens
	if e := c.request(ctx, "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": password}, &t); e != nil {
		return nil, false, e
	}
	s := &Session{client: c}
	if t.Requires2FA && t.TempToken != "" {
		s.challenge = t.TempToken
		return s, true, nil
	}
	if t.Access == "" {
		return nil, false, errors.New("登录需要额外验证，当前无法完成")
	}
	s.setTokens(t)
	return s, false, nil
}
func (s *Session) setTokens(t Tokens) {
	s.access = t.Access
	if t.Refresh != "" {
		s.refresh = t.Refresh
	}
	s.expires = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	if t.User.Email != "" {
		s.User = User{t.User.Email, t.User.Username}
	}
}
func (s *Session) CompleteOTP(ctx context.Context, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.challenge == "" {
		return errors.New("没有待完成的登录验证")
	}
	var t Tokens
	if e := s.client.request(ctx, "POST", "/api/v1/auth/login/2fa", "", map[string]string{"temp_token": s.challenge, "totp_code": code}, &t); e != nil {
		return e
	}
	if t.Access == "" {
		return errors.New("二次验证失败")
	}
	s.setTokens(t)
	s.challenge = ""
	return nil
}

// Resume exchanges a saved refresh token before using any authenticated endpoint.
func (c *Client) Resume(ctx context.Context, refresh string) (*Session, error) {
	s := &Session{client: c, refresh: refresh}
	if err := s.refreshToken(ctx); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Session) Remember(save func(string) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refresh == "" {
		return errors.New("网关未提供刷新令牌，无法记住登录状态")
	}
	if err := save(s.refresh); err != nil {
		return err
	}
	s.onRefresh = save
	return nil
}
func (s *Session) refreshToken(ctx context.Context) error {
	if s.refresh == "" {
		return &SessionExpiredError{Cause: errors.New("网关未提供可用的刷新令牌")}
	}
	var t Tokens
	if e := s.client.request(ctx, "POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": s.refresh}, &t); e != nil {
		if rejectedAuthentication(e) {
			return &SessionExpiredError{Cause: e}
		}
		return e
	}
	if t.Access == "" {
		return errors.New("登录刷新失败")
	}
	s.setTokens(t)
	if s.onRefresh != nil {
		return s.onRefresh(s.refresh)
	}
	return nil
}
func (s *Session) request(ctx context.Context, method, path string, in, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.access == "" {
		return errors.New("请先完成登录")
	}
	if time.Now().After(s.expires.Add(-30 * time.Second)) {
		if e := s.refreshToken(ctx); e != nil {
			return e
		}
	}
	e := s.client.request(ctx, method, path, s.access, in, out)
	var remote *RemoteError
	if errors.As(e, &remote) && remote.Status == 401 {
		if e = s.refreshToken(ctx); e != nil {
			return e
		}
		e = s.client.request(ctx, method, path, s.access, in, out)
		if errors.As(e, &remote) && remote.Status == http.StatusUnauthorized {
			return &SessionExpiredError{Cause: e}
		}
		return e
	}
	return e
}
func (s *Session) Profile(ctx context.Context) (User, error) {
	var u struct {
		Email    string `json:"email"`
		Username string `json:"username"`
	}
	e := s.request(ctx, "GET", "/api/v1/user/profile", nil, &u)
	return User{u.Email, u.Username}, e
}
func (s *Session) Groups(ctx context.Context) ([]Group, error) {
	g := []Group{}
	e := s.request(ctx, "GET", "/api/v1/groups/available", nil, &g)
	return g, e
}
func (s *Session) Keys(ctx context.Context) ([]Key, error) {
	out := []Key{}
	for page := 1; page <= 100; page++ {
		var v struct {
			Items []Key `json:"items"`
			Pages int   `json:"pages"`
		}
		if e := s.request(ctx, "GET", fmt.Sprintf("/api/v1/keys?page=%d&page_size=100", page), nil, &v); e != nil {
			return nil, e
		}
		out = append(out, v.Items...)
		if page >= v.Pages || len(v.Items) == 0 {
			return out, nil
		}
	}
	return nil, errors.New("Key 数量超出客户端读取上限")
}
func (s *Session) CreateKey(ctx context.Context, group int64) (Key, error) {
	var k Key
	e := s.request(ctx, "POST", "/api/v1/keys", map[string]any{"name": "SurplusToken Connect", "group_id": group}, &k)
	return k, e
}
func (s *Session) Models(ctx context.Context, k Key) ([]string, error) {
	if !k.Usable() {
		return nil, errors.New("Key 已停用、过期或无法读取")
	}
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if e := s.client.request(ctx, "GET", "/v1/models", k.Value, nil, &v); e != nil {
		return nil, e
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range v.Data {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}
func (s *Session) Logout(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refresh == "" {
		return nil
	}
	return s.client.request(ctx, "POST", "/api/v1/auth/logout", s.access, map[string]string{"refresh_token": s.refresh}, nil)
}

// ValidateOrigin is used by tests to ensure tokens cannot follow redirects.
func ValidateOrigin(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host == "surplustoken.com" && u.User == nil
}
func (s *Session) Key(ctx context.Context, id int64) (Key, error) {
	var k Key
	e := s.request(ctx, "GET", fmt.Sprintf("/api/v1/keys/%d", id), nil, &k)
	return k, e
}
