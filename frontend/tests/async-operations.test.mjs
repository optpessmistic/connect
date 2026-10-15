import assert from "node:assert/strict";
import { test } from "node:test";
import { AsyncOperations } from "../src/async-operations.ts";

test("the newest history refresh owns results and loading completion", () => {
  const operations = new AsyncOperations();
  const slow = operations.begin("history", false);
  const fresh = operations.begin("history", false);
  assert.equal(slow.isCurrent(), false);
  assert.equal(fresh.isCurrent(), true);
  assert.equal(slow.finish(), false);
  assert.equal(fresh.isCurrent(), true);
  assert.equal(operations.busy, false);
  assert.equal(fresh.finish(), true);
});

test("old model completion after a session reset cannot overwrite a new login or clear its busy state", () => {
  const operations = new AsyncOperations();
  const models = operations.begin("models");
  operations.reset();
  const login = operations.begin("auth");
  assert.equal(models.isCurrent(), false);
  assert.equal(models.finish(), false);
  assert.equal(operations.busy, true);
  assert.equal(login.isCurrent(), true);
  login.finish();
  assert.equal(operations.busy, false);
});

test("a background refresh never clears an unrelated blocking apply operation", () => {
  const operations = new AsyncOperations();
  const apply = operations.begin("apply");
  const history = operations.begin("history", false);
  history.finish();
  assert.equal(operations.busy, true);
  apply.finish();
  assert.equal(operations.busy, false);
});

test("a superseded operation cannot keep the interface busy after its replacement finishes", () => {
  const operations = new AsyncOperations();
  const slow = operations.begin("models");
  const retry = operations.begin("models");
  retry.finish();
  assert.equal(operations.busy, false);
  assert.equal(slow.isCurrent(), false);
  assert.equal(slow.finish(), false);
});

test("reset invalidates all operation kinds and immediately releases the old busy state", () => {
  const operations = new AsyncOperations();
  const active = [operations.begin("plan"), operations.begin("keys"), operations.begin("history", false)];
  operations.reset();
  assert.equal(operations.busy, false);
  for (const operation of active) {
    assert.equal(operation.isCurrent(), false);
    assert.equal(operation.finish(), false);
  }
});
