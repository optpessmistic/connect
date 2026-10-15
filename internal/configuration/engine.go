// Package configuration owns registered client files, never arbitrary paths
// supplied by the frontend. Each transaction currently changes one file.
package configuration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"surplustoken-connect/internal/configedit"
	"surplustoken-connect/internal/discovery"
	"surplustoken-connect/internal/privatefile"
)

type Client struct {
	Detection discovery.Result `json:"detection"`
	ID        string           `json:"id"`
	Path      string           `json:"path"`
	Installed bool             `json:"installed"`
	Supported bool             `json:"supported"`
	Version   string           `json:"version"`
	Reason    string           `json:"reason"`
}
type Change struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}
type Plan struct {
	ID             string          `json:"id"`
	Client         string          `json:"client"`
	Path           string          `json:"path"`
	Changes        []Change        `json:"changes"`
	Noop           bool            `json:"noop"`
	Created        time.Time       `json:"created"`
	Owner          string          `json:"-"`
	Before, After  []byte          `json:"-"`
	Existed        bool            `json:"-"`
	Delete         bool            `json:"-"`
	Patches        []configedit.KV `json:"-"`
	DeletedFields  []string        `json:"-"`
	CreatedObjects []string        `json:"-"`
	SourceID       string          `json:"-"`
	managed        *managedChange
}
type Record struct {
	ID        string    `json:"id"`
	Client    string    `json:"client"`
	Path      string    `json:"path"`
	At        time.Time `json:"at"`
	State     string    `json:"state"`
	Operation string    `json:"operation"`
	Changes   []Change  `json:"changes"`
	Backup    string    `json:"backup"`
}
type diskRecord struct {
	Record
	Before         []byte         `json:"before_bytes"`
	After          []byte         `json:"after_bytes"`
	Existed        bool           `json:"existed"`
	AfterExists    bool           `json:"after_exists"`
	Fields         []string       `json:"fields"`
	Kind           string         `json:"kind"`
	SourceID       string         `json:"source_id,omitempty"`
	CreatedObjects []string       `json:"created_objects,omitempty"`
	Managed        *managedChange `json:"managed,omitempty"`
}
type Engine struct {
	mu    sync.Mutex
	Store string
	Paths map[string]string
	// Discover permits fixture mode to avoid probing installed client programs.
	Discover func() map[string]discovery.Result
	plans    map[string]*Plan
	lock     *flock.Flock
}

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func Default() (*Engine, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return nil, e
	}
	config, e := os.UserConfigDir()
	if e != nil {
		return nil, e
	}
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	oc := os.Getenv("OPENCODE_CONFIG_DIR")
	if oc == "" {
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		oc = filepath.Join(xdg, "opencode")
	}
	ocfile := ""
	if ocfile == "" {
		ocfile = filepath.Join(oc, "opencode.json")
		if _, e := os.Stat(filepath.Join(oc, "opencode.jsonc")); e == nil {
			ocfile = filepath.Join(oc, "opencode.jsonc")
		}
	}
	return New(filepath.Join(config, "SurplusToken Connect", "backups"), map[string]string{"claude": filepath.Join(claude, "settings.json"), "opencode": ocfile})
}
func New(store string, paths map[string]string) (*Engine, error) {
	if e := os.MkdirAll(store, 0700); e != nil {
		return nil, e
	}
	if e := privatefile.Directory(store); e != nil {
		return nil, e
	}
	for k, p := range paths {
		a, e := filepath.Abs(p)
		if e != nil {
			return nil, e
		}
		paths[k] = a
	}
	e := &Engine{Store: store, Paths: paths, plans: map[string]*Plan{}, lock: flock.New(filepath.Join(store, ".lock"))}
	return e, nil
}
func (e *Engine) Detect() []Client {
	var found map[string]discovery.Result
	if e.Discover != nil {
		found = e.Discover()
	} else {
		found = discovery.Detect(discovery.Default())
	}
	out := []Client{}
	for _, id := range []string{"codex", "claude", "omp", "opencode", "kimi"} {
		result := found[id]
		c := Client{ID: id, Path: e.Paths[id], Installed: result.Installed, Version: result.Version, Detection: result, Supported: id == "claude" || id == "opencode"}
		if id == "codex" {
			c.Reason = "当前阶段保护 Codex，不执行、不读取或修改其配置"
		}
		if id == "opencode" && c.Installed {
			if c.Version == "" {
				c.Supported = false
				c.Reason = "无法确认 OpenCode 配置版本"
			} else if strings.Split(c.Version, ".")[0] != "2" {
				c.Supported = false
				c.Reason = "OpenCode v" + strings.Split(c.Version, ".")[0] + " 配置格式需独立适配，当前仅支持 v2"
			}
		}
		if !c.Supported && c.Reason == "" {
			c.Reason = "此客户端的写入适配尚未接入"
		}
		out = append(out, c)
	}
	return out
}
func safePath(p string) error {
	home, _ := os.UserHomeDir()
	protected := []string{filepath.Join(home, ".codex")}
	if custom := os.Getenv("CODEX_HOME"); custom != "" {
		protected = append(protected, custom)
	}
	for _, root := range protected {
		root, _ = filepath.Abs(root)
		if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("拒绝访问受保护的 Codex 目录")
		}
	}

	for q := p; ; q = filepath.Dir(q) {
		info, er := os.Lstat(q)
		if er == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("配置路径含符号链接，当前版本拒绝写入")
			}
			if q == p && !info.Mode().IsRegular() {
				return errors.New("配置不是普通文件")
			}
		} else if !os.IsNotExist(er) {
			return errors.New("无法访问配置路径")
		}
		if filepath.Dir(q) == q {
			break
		}
	}
	return nil
}
func read(p string) ([]byte, bool, error) {
	if e := safePath(p); e != nil {
		return nil, false, e
	}
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, errors.New("无法读取配置文件")
	}
	if len(b) > 4<<20 {
		return nil, false, errors.New("配置文件过大")
	}
	return b, true, nil
}
func hash(b []byte, exists bool) string {
	if !exists {
		return "absent"
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func value(raw []byte, path string) gjson.Result {
	b := bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	return gjson.GetBytes(jsonc.ToJSON(b), path)
}
func shown(v gjson.Result, field string) string {
	if !v.Exists() {
		return "未设置"
	}
	low := strings.ToLower(field[strings.LastIndex(field, ".")+1:])
	if strings.Contains(low, "key") || strings.Contains(low, "token") {
		if v.String() == "" {
			return "空值"
		}
		return "••••••••"
	}
	if v.IsObject() {
		return "结构化配置"
	}
	s := v.String()
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
func same(a, b gjson.Result) bool {
	if a.Exists() != b.Exists() {
		return false
	}
	if !a.Exists() {
		return true
	}
	var x, y any
	if json.Unmarshal([]byte(a.Raw), &x) != nil || json.Unmarshal([]byte(b.Raw), &y) != nil {
		return a.Raw == b.Raw
	}
	p, _ := json.Marshal(x)
	q, _ := json.Marshal(y)
	return bytes.Equal(p, q)
}
func (e *Engine) Plan(owner, client string, patches []configedit.KV) (Plan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.planLocked(owner, client, patches, "")
}

// PlanManaged reconciles fields previously written within scope. Fields that
// disappear from the new template return to their pre-Connect values; unrelated
// fields and user-created model properties are never claimed or removed.
func (e *Engine) PlanManaged(owner, client string, patches []configedit.KV, scope string) (Plan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if scope == "" {
		return Plan{}, errors.New("受管配置范围不能为空")
	}
	var plan Plan
	err := e.withLock(func() error {
		var err error
		plan, err = e.planLocked(owner, client, patches, scope)
		return err
	})
	return plan, err
}

func (e *Engine) planLocked(owner, client string, patches []configedit.KV, scope string) (Plan, error) {
	path, ok := e.Paths[client]
	if !ok || client == "codex" {
		return Plan{}, errors.New("客户端尚未开放配置写入")
	}
	raw, exists, err := read(path)
	if err != nil {
		return Plan{}, err
	}
	if exists {
		if err = validateJSON(raw); err != nil {
			return Plan{}, errors.New("现有 JSON/JSONC 无效或包含重复字段，未修改文件")
		}
	}
	patches = append([]configedit.KV(nil), patches...)
	var deleted []string
	var managed *managedChange
	if scope != "" {
		patches, deleted, managed, err = e.reconcileManaged(client, scope, raw, patches)
		if err != nil {
			return Plan{}, err
		}
	}
	for _, p := range patches {
		for _, ancestor := range configedit.ObjectAncestors(p.Path) {
			r := value(raw, ancestor)
			if r.Exists() && !r.IsObject() {
				return Plan{}, fmt.Errorf("配置字段 %s 不是对象，拒绝覆盖", ancestor)
			}
		}
	}
	after, err := configedit.PatchJSON(raw, patches, deleted)
	if err != nil {
		return Plan{}, errors.New("现有 JSON/JSONC 配置无效，未修改文件")
	}
	fields := patchFields(patches, deleted)
	created := createdObjects(raw, after, fields)
	if managed != nil {
		after, err = configedit.PruneEmptyObjects(after, managed.Before.Objects)
		if err != nil {
			return Plan{}, errors.New("无法清理旧的受管配置")
		}
		managed.After.Objects = survivingObjects(after, append(append([]string(nil), managed.Before.Objects...), created...))
	}
	if err := validateJSON(after); err != nil {
		return Plan{}, errors.New("生成的 JSON/JSONC 配置无效，未修改文件")
	}
	plan := Plan{ID: ID(), Client: client, Path: path, Owner: owner, Before: raw, After: after, Existed: exists, Patches: patches, DeletedFields: deleted, CreatedObjects: created, managed: managed, Created: time.Now(), Changes: []Change{}}
	for _, field := range fields {
		before, next := value(raw, field), value(after, field)
		if !same(before, next) {
			plan.Changes = append(plan.Changes, Change{field, shown(before, field), shown(next, field)})
		}
	}
	plan.Noop = len(plan.Changes) == 0
	if managed != nil && len(managed.Before.Fields) > 0 && !sameManaged(managed.Before, managed.After) {
		plan.Noop = false // A released field must also be released in the journal.
	}
	for id, p := range e.plans {
		if time.Since(p.Created) > 10*time.Minute {
			delete(e.plans, id)
		}
	}
	if len(e.plans) >= 100 {
		return Plan{}, errors.New("待应用计划过多，请稍后重试")
	}
	e.plans[plan.ID] = &plan
	return plan, nil
}
func atomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".connect-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = privatefile.File(f); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err == nil {
		if d, er := os.Open(filepath.Dir(path)); er == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return err
}
func (e *Engine) save(r diskRecord) error {
	b, er := json.MarshalIndent(r, "", "  ")
	if er != nil {
		return er
	}
	return atomic(filepath.Join(e.Store, r.ID+".json"), b)
}
func (e *Engine) applyLocked(owner, id string) (Record, error) {
	p, ok := e.plans[id]
	if !ok || p.Owner != owner || time.Since(p.Created) > 10*time.Minute {
		return Record{}, errors.New("变更计划已失效，请重新预览")
	}
	if p.Noop && p.SourceID == "" {
		delete(e.plans, id)
		return Record{Client: p.Client, Path: p.Path, State: "unchanged", Changes: []Change{}}, nil
	}
	cur, exists, er := read(p.Path)
	if er != nil {
		return Record{}, er
	}
	if hash(cur, exists) != hash(p.Before, p.Existed) {
		return Record{}, errors.New("配置在预览后被修改，请重新预览")
	}
	if p.managed != nil {
		current, err := e.managedStateLocked(p.Client, p.managed.Scope)
		if err != nil {
			return Record{}, err
		}
		if !sameManaged(current, p.managed.Before) {
			return Record{}, errors.New("受管配置记录在预览后已变化，请重新预览")
		}
	}
	r := diskRecord{Record: Record{ID: ID(), Client: p.Client, Path: p.Path, At: time.Now(), State: "pending", Changes: p.Changes}, Before: p.Before, After: p.After, Existed: p.Existed, AfterExists: !p.Delete, Kind: "apply", SourceID: p.SourceID, CreatedObjects: p.CreatedObjects, Managed: p.managed}
	r.Operation = "apply"
	if p.SourceID != "" {
		r.Operation = "restore"
	}
	r.Backup = filepath.Join(e.Store, r.ID+".json")
	r.Fields = patchFields(p.Patches, p.DeletedFields)
	if er = e.save(r); er != nil {
		return Record{}, errors.New("备份失败，未修改配置")
	}
	if p.Noop {
		// A completed manual restore still needs its ownership/history update.
	} else if p.Delete {
		er = os.Remove(p.Path)
	} else {
		er = atomic(p.Path, p.After)
	}
	if er != nil {
		r.State = "failed"
		_ = e.save(r)
		return Record{}, errors.New("配置写入失败，操作前备份已保存")
	}
	r.State = "applied"
	if er = e.save(r); er != nil {
		return Record{}, errors.New("配置已写入但记录未完成，下次打开将核对恢复状态")
	}
	if r.SourceID != "" {
		if source, err := e.load(r.SourceID); err == nil {
			source.State = "reverted"
			_ = e.save(source)
		}
	}
	delete(e.plans, id)
	return r.Record, nil
}
func (e *Engine) withLock(fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ok, er := e.lock.TryLockContext(ctx, 50*time.Millisecond)
	if er != nil || !ok {
		return errors.New("另一个 Connect 正在修改配置，请稍后重试")
	}
	defer e.lock.Unlock()
	return fn()
}
func (e *Engine) Apply(owner, id string) (Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var r Record
	er := e.withLock(func() error { var err error; r, err = e.applyLocked(owner, id); return err })
	return r, er
}
func (e *Engine) load(id string) (diskRecord, error) {
	if len(id) != 32 {
		return diskRecord{}, errors.New("记录不存在")
	}
	if _, er := hex.DecodeString(id); er != nil {
		return diskRecord{}, errors.New("记录不存在")
	}
	b, er := os.ReadFile(filepath.Join(e.Store, id+".json"))
	if er != nil {
		return diskRecord{}, errors.New("无法读取备份")
	}
	var r diskRecord
	if json.Unmarshal(b, &r) != nil || r.ID != id || e.Paths[r.Client] != r.Path || r.Client == "codex" {
		return diskRecord{}, errors.New("备份记录无效")
	}
	return r, nil
}
func (e *Engine) History() ([]Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Record{}
	er := e.withLock(func() error {
		files, err := os.ReadDir(e.Store)
		if err != nil {
			return errors.New("无法读取配置记录")
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			r, err := e.load(strings.TrimSuffix(f.Name(), ".json"))
			if err != nil {
				continue
			}
			if r.State == "pending" {
				cur, exists, err := read(r.Path)
				if err == nil {
					switch hash(cur, exists) {
					case hash(r.After, r.AfterExists):
						r.State = "applied"
					case hash(r.Before, r.Existed):
						r.State = "failed"
					default:
						r.State = "conflict"
					}
					_ = e.save(r)
				}
			}
			if r.State == "applied" && r.SourceID != "" {
				if source, err := e.load(r.SourceID); err == nil && source.State == "applied" {
					source.State = "reverted"
					_ = e.save(source)
				}
			}
			out = append(out, r.Record)
		}
		return nil
	})
	for i := range out {
		if r, err := e.load(out[i].ID); err == nil {
			out[i] = r.Record
		}
	}
	return out, er
}
func (e *Engine) Restore(owner, id string) (Plan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var plan Plan
	err := e.withLock(func() error {
		var err error
		plan, err = e.restoreLocked(owner, id)
		return err
	})
	return plan, err
}

func (e *Engine) restoreLocked(owner, id string) (Plan, error) {
	r, er := e.load(id)
	if er != nil {
		return Plan{}, er
	}
	if r.Kind != "apply" || r.State != "applied" || r.Operation == "restore" {
		return Plan{}, errors.New("此记录不能恢复")
	}
	cur, exists, er := read(r.Path)
	if er != nil {
		return Plan{}, er
	}
	if exists {
		if er = validateJSON(cur); er != nil {
			return Plan{}, errors.New("当前配置无效或包含重复字段，未恢复")
		}
	}
	if !exists {
		return Plan{}, errors.New("配置已被删除，恢复前需要重新检查")
	}
	set := []configedit.KV{}
	del := []string{}
	changes := []Change{}
	for _, f := range r.Fields {
		now, applied, orig := value(cur, f), value(r.After, f), value(r.Before, f)
		if same(now, orig) {
			continue
		}
		if !same(now, applied) {
			return Plan{}, fmt.Errorf("字段 %s 已被其他程序修改，未恢复", f)
		}
		if orig.Exists() {
			for _, ancestor := range configedit.ObjectAncestors(f) {
				if parent := value(cur, ancestor); parent.Exists() && !parent.IsObject() {
					return Plan{}, fmt.Errorf("字段 %s 已被其他程序改为非对象，未恢复", ancestor)
				}
			}
			var v any
			decoder := json.NewDecoder(strings.NewReader(orig.Raw))
			decoder.UseNumber()
			if decoder.Decode(&v) != nil {
				return Plan{}, errors.New("备份字段无效")
			}
			set = append(set, configedit.KV{Path: f, Value: v})
		} else {
			del = append(del, f)
		}
		changes = append(changes, Change{f, shown(now, f), shown(orig, f)})
	}
	next, er := configedit.PatchJSON(cur, set, del)
	if er != nil {
		return Plan{}, errors.New("无法生成恢复计划")
	}
	created := r.CreatedObjects
	if created == nil {
		created = createdObjects(r.Before, r.After, r.Fields)
	}
	next, er = configedit.PruneEmptyObjects(next, created)
	if er != nil || validateJSON(next) != nil {
		return Plan{}, errors.New("无法安全清理恢复后的空配置对象")
	}
	changes = append(changes, removedObjectChanges(cur, next, created)...)
	var managed *managedChange
	scope := ""
	if r.Managed != nil {
		scope = r.Managed.Scope
	} else if r.Client == "opencode" {
		for _, field := range r.Fields {
			if within(field, "providers.surplustoken") {
				scope = "providers.surplustoken"
				break
			}
		}
	}
	if scope != "" {
		current, checkpoint, err := e.managedCheckpointLocked(r.Client, scope, r.ID)
		if err != nil {
			return Plan{}, err
		}
		if checkpoint == nil || !sameManaged(current, checkpoint.After) {
			return Plan{}, errors.New("受管配置已有后续变更，请先恢复最近的配置操作")
		}
		managed = &managedChange{Scope: scope, Before: current, After: cloneManaged(checkpoint.Before)}
	}
	p := Plan{ID: ID(), Client: r.Client, Path: r.Path, Owner: owner, Created: time.Now(), Before: cur, After: next, Existed: true, Changes: changes, Noop: len(changes) == 0, SourceID: r.ID, managed: managed}
	if managed != nil && !sameManaged(managed.Before, managed.After) {
		p.Noop = false
	}
	if hash(cur, true) == hash(r.After, r.AfterExists) {
		if r.Existed {
			p.After = r.Before
		} else {
			p.After = nil
			p.Delete = true
		}
	}
	if len(e.plans) >= 100 {
		return Plan{}, errors.New("待应用计划过多，请稍后重试")
	}
	for _, f := range r.Fields {
		p.Patches = append(p.Patches, configedit.KV{Path: f})
	}
	e.plans[p.ID] = &p
	return p, nil
}
func (e *Engine) Forget(owner string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, p := range e.plans {
		if p.Owner == owner {
			delete(e.plans, id)
		}
	}
}
