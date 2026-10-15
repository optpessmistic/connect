package configuration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"surplustoken-connect/internal/configedit"
)

// Ownership is kept in the same private transaction record as its file bytes.
// A nil Before means the field did not exist before Connect first claimed it.
type managedValue struct {
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after"`
}

type managedState struct {
	Fields  map[string]managedValue `json:"fields"`
	Objects []string                `json:"objects,omitempty"`
}

type managedChange struct {
	Scope  string       `json:"scope"`
	Before managedState `json:"before"`
	After  managedState `json:"after"`
}

func cloneManaged(state managedState) managedState {
	next := managedState{Fields: map[string]managedValue{}, Objects: append([]string(nil), state.Objects...)}
	for field, owned := range state.Fields {
		next.Fields[field] = owned
	}
	return next
}

func sameManaged(a, b managedState) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func within(field, scope string) bool {
	return field == scope || strings.HasPrefix(field, scope+".")
}

func snapshot(v gjson.Result) json.RawMessage {
	if !v.Exists() {
		return nil
	}
	return json.RawMessage(v.Raw)
}

func rawValue(raw json.RawMessage) gjson.Result {
	if len(raw) == 0 {
		return gjson.Result{}
	}
	return gjson.ParseBytes(raw)
}

func patchFields(set []configedit.KV, del []string) []string {
	seen := map[string]bool{}
	fields := []string{}
	for _, patch := range set {
		if !seen[patch.Path] {
			fields = append(fields, patch.Path)
			seen[patch.Path] = true
		}
	}
	for _, field := range del {
		if !seen[field] {
			fields = append(fields, field)
			seen[field] = true
		}
	}
	return fields
}

func createdObjects(before, after []byte, fields []string) []string {
	seen := map[string]bool{}
	objects := []string{}
	for _, field := range fields {
		for _, parent := range configedit.ObjectAncestors(field) {
			if !seen[parent] && !value(before, parent).Exists() && value(after, parent).IsObject() {
				objects = append(objects, parent)
				seen[parent] = true
			}
		}
	}
	return objects
}

func survivingObjects(raw []byte, objects []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, parent := range objects {
		if !seen[parent] && value(raw, parent).IsObject() {
			result = append(result, parent)
			seen[parent] = true
		}
	}
	sort.Strings(result)
	return result
}

func removedObjectChanges(before, after []byte, objects []string) []Change {
	removed := map[string]bool{}
	for _, path := range objects {
		if value(before, path).IsObject() && !value(after, path).Exists() {
			removed[path] = true
		}
	}
	changes := []Change{}
	for _, path := range objects {
		if !removed[path] {
			continue
		}
		nested := false
		for _, parent := range configedit.ObjectAncestors(path) {
			if removed[parent] {
				nested = true
			}
		}
		if !nested {
			changes = append(changes, Change{path, "空配置对象", "未设置"})
		}
	}
	return changes
}

// Read the ownership journal in transaction order. Legacy records already carry
// field-level before/after snapshots, so existing installations can migrate
// without guessing from the current provider or claiming user-created models.
func (e *Engine) managedStateLocked(client, scope string) (managedState, error) {
	state, _, err := e.managedCheckpointLocked(client, scope, "")
	return state, err
}

func (e *Engine) managedCheckpointLocked(client, scope, recordID string) (managedState, *managedChange, error) {
	state := managedState{Fields: map[string]managedValue{}}
	var checkpoint *managedChange
	files, err := os.ReadDir(e.Store)
	if err != nil {
		return state, nil, errors.New("无法读取受管配置记录")
	}
	records := []diskRecord{}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		// Backups from another configured directory remain on disk, but do not
		// belong to this registered target and must not affect its ownership.
		raw, readErr := os.ReadFile(filepath.Join(e.Store, file.Name()))
		var target struct{ Client, Path string }
		if readErr != nil || json.Unmarshal(raw, &target) != nil {
			return state, nil, errors.New("配置记录损坏，无法安全核对受管字段")
		}
		if target.Client != client || target.Path != e.Paths[client] {
			continue
		}
		r, err := e.load(strings.TrimSuffix(file.Name(), ".json"))
		if err != nil {
			// An unreadable transaction must not silently release its fields.
			return state, nil, errors.New("配置记录损坏，无法安全核对受管字段")
		}
		if r.Client != client || r.Kind != "apply" {
			continue
		}
		if r.State == "pending" {
			current, exists, err := read(r.Path)
			if err != nil {
				return state, nil, err
			}
			if hash(current, exists) == hash(r.After, r.AfterExists) {
				r.State = "applied"
			} else if hash(current, exists) != hash(r.Before, r.Existed) {
				return state, nil, errors.New("存在未完成且冲突的配置记录，请先检查恢复状态")
			}
		}
		if r.State == "applied" || r.State == "reverted" {
			records = append(records, r)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].At.Equal(records[j].At) {
			return records[i].ID < records[j].ID
		}
		return records[i].At.Before(records[j].At)
	})
	beforeRecords := map[string]managedState{}
	for _, r := range records {
		beforeRecords[r.ID] = cloneManaged(state)
		if r.Managed != nil && r.Managed.Scope == scope {
			state = cloneManaged(r.Managed.After)
		} else if r.SourceID != "" {
			previous, ok := beforeRecords[r.SourceID]
			if !ok {
				return state, nil, errors.New("缺少恢复来源记录，无法安全核对受管字段")
			}
			state = cloneManaged(previous)
		} else {
			ownedFields := []string{}
			for _, field := range r.Fields {
				if !within(field, scope) {
					continue
				}
				ownedFields = append(ownedFields, field)
				after := value(r.After, field)
				if !after.Exists() {
					delete(state.Fields, field)
					continue
				}
				owned, ok := state.Fields[field]
				if !ok {
					owned.Before = snapshot(value(r.Before, field))
				}
				owned.After = snapshot(after)
				state.Fields[field] = owned
			}
			state.Objects = survivingObjects(r.After, append(state.Objects, createdObjects(r.Before, r.After, ownedFields)...))
		}
		if r.ID == recordID {
			checkpoint = &managedChange{Scope: scope, Before: beforeRecords[r.ID], After: cloneManaged(state)}
		}
	}
	return state, checkpoint, nil
}

func (e *Engine) reconcileManaged(client, scope string, raw []byte, patches []configedit.KV) ([]configedit.KV, []string, *managedChange, error) {
	before, err := e.managedStateLocked(client, scope)
	if err != nil {
		return nil, nil, nil, err
	}
	after := cloneManaged(before)
	desired := map[string]bool{}
	for _, patch := range patches {
		if within(patch.Path, scope) {
			desired[patch.Path] = true
		}
	}
	fields := make([]string, 0, len(before.Fields))
	for field := range before.Fields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	deleted := []string{}
	for _, field := range fields {
		owned := before.Fields[field]
		if !same(value(raw, field), rawValue(owned.After)) {
			return nil, nil, nil, fmt.Errorf("受管字段 %s 已被其他程序修改，未覆盖或清理", field)
		}
		if desired[field] {
			continue
		}
		if len(owned.Before) == 0 {
			deleted = append(deleted, field)
		} else {
			var original any
			decoder := json.NewDecoder(bytes.NewReader(owned.Before))
			decoder.UseNumber()
			if err := decoder.Decode(&original); err != nil {
				return nil, nil, nil, errors.New("受管字段的原始备份无效")
			}
			patches = append(patches, configedit.KV{Path: field, Value: original})
		}
		delete(after.Fields, field)
	}
	for _, patch := range patches {
		if !desired[patch.Path] {
			continue
		}
		owned, ok := after.Fields[patch.Path]
		if !ok {
			owned.Before = snapshot(value(raw, patch.Path))
		}
		owned.After, err = json.Marshal(patch.Value)
		if err != nil {
			return nil, nil, nil, errors.New("受管配置字段无效")
		}
		after.Fields[patch.Path] = owned
	}
	return patches, deleted, &managedChange{Scope: scope, Before: before, After: after}, nil
}
