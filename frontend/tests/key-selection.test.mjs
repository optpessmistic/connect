import assert from "node:assert/strict";
import { test } from "node:test";
import { compatibleClientKeys, initialKeySelections, selectClientKey, validKeySelections } from "../src/key-selection.ts";

const groups = [
  {id:1, name:"Messages", platform:"anthropic", allow_messages_dispatch:false, status:"active"},
  {id:2, name:"Responses", platform:"openai", allow_messages_dispatch:false, status:"active"},
];
const key = (id, group_id = 1, extra = {}) => ({id, group_id, name:`Key ${id}`, mask:`••••${id}`, status:"active", ...extra});

test("changing one client's key preserves another client's choice and the previous state", () => {
  const initial = initialKeySelections(["claude", "opencode"], [key(1)], groups);
  assert.deepEqual(initial, {claude:"1", opencode:"1"});
  const after = selectClientKey(initial, "claude", "2");
  assert.deepEqual(after, {claude:"2", opencode:"1"});
  assert.deepEqual(initial, {claude:"1", opencode:"1"});
  assert.deepEqual(selectClientKey(after, "opencode", "3"), {claude:"2", opencode:"3"});
});

test("automatic choices are per client and only made when one compatible key exists", () => {
  assert.deepEqual(initialKeySelections(["claude", "opencode"], [key(1), key(2, 2)], groups), {claude:"1"});
  assert.deepEqual(initialKeySelections(["claude", "opencode"], [key(1), key(2)], groups), {});
});

test("compatibility excludes expired, disabled, missing-group and unsupported-client keys", () => {
  const now = Date.parse("2026-10-14T12:00:00Z");
  const keys = [key(1), key(2, 2), key(3, 1, {status:"disabled"}), key(4, 1, {expires_at:"2026-10-14T12:00:00Z"}), key(5, 999)];
  assert.deepEqual(compatibleClientKeys("claude", keys, groups, now).map(key => key.id), [1]);
  assert.deepEqual(compatibleClientKeys("opencode", keys, groups, now).map(key => key.id), [1, 2]);
  assert.deepEqual(compatibleClientKeys("codex", keys, groups, now), []);
});

test("refresh invalidates bindings after key revocation or a group's protocol changes", () => {
  const selections = {claude:"1", opencode:"2"};
  assert.deepEqual(validKeySelections(selections, ["claude", "opencode"], [key(1, 1, {status:"disabled"}), key(2, 2)], groups), {opencode:"2"});
  const changedGroups = [{...groups[0], platform:"openai"}, groups[1]];
  assert.deepEqual(validKeySelections(selections, ["claude", "opencode"], [key(1), key(2, 2)], changedGroups), {opencode:"2"});
  assert.deepEqual(validKeySelections(selections, ["claude"], [key(1), key(2, 2)], groups), {claude:"1"});
});
