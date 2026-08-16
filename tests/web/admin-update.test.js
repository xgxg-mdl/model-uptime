import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  UPDATE_TARGET_KEY,
  createUpdateController,
  renderUpdateError,
  renderUpdateStatus,
} from '../../internal/httpserver/web/assets/scripts/admin/updater.js';
import { createElementDocument } from './helpers/fake-dom.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const html = fs.readFileSync(path.join(root, 'internal/httpserver/web/admin/index.html'), 'utf8');
const updateIDs = [
  'update-current',
  'update-latest',
  'update-status',
  'update-detail',
  'update-check-btn',
  'update-start-btn',
];

test('管理页保留完整更新控件', () => {
  for (const id of updateIDs) assert.match(html, new RegExp(`id="${id}"`));
});

test('更新状态渲染按钮、版本和错误信息', () => {
  const document = createElementDocument(updateIDs);
  renderUpdateStatus(document, {
    current_version: 'v1.0.0',
    latest_version: 'v1.1.0',
    enabled: true,
    update_available: true,
    updating: false,
    deployment_tag: 'latest',
  });

  assert.equal(document.getElementById('update-current').textContent, 'v1.0.0');
  assert.equal(document.getElementById('update-latest').textContent, 'v1.1.0');
  assert.equal(document.getElementById('update-status').textContent, 'Update available');
  assert.ok(document.getElementById('update-status').classList.contains('warn'));
  assert.equal(document.getElementById('update-start-btn').disabled, false);
  assert.match(document.getElementById('update-detail').textContent, /Deployment tag: latest/);

  renderUpdateError(document, 'Waiting for service.', true);
  assert.equal(document.getElementById('update-status').textContent, 'Restarting service…');
  assert.equal(document.getElementById('update-detail').textContent, 'Waiting for service.');
  assert.equal(document.getElementById('update-start-btn').disabled, true);
});

test('更新控制器检查、触发并轮询到目标版本', async () => {
  const document = createElementDocument(updateIDs);
  const scheduled = [];
  const calls = [];
  const toasts = [];
  const values = new Map();
  let currentVersion = 'v1.0.0';
  const storage = {
    getItem: key => values.get(key) || null,
    setItem: (key, value) => values.set(key, value),
    removeItem: key => values.delete(key),
  };
  const available = () => ({
    current_version: currentVersion,
    latest_version: 'v1.1.0',
    enabled: true,
    update_available: currentVersion !== 'v1.1.0',
    updating: false,
  });
  const api = async (requestPath, options = {}) => {
    calls.push([requestPath, options]);
    if (requestPath === '/api/admin/update' && options.method === 'POST') {
      return { target_version: 'v1.1.0' };
    }
    return available();
  };
  const controller = createUpdateController({
    document,
    api,
    toast: message => toasts.push(message),
    storage,
    confirm: () => true,
    schedule(callback, delay) {
      scheduled.push({ callback, delay });
      return scheduled.length;
    },
    cancel() {},
    now: () => 1_000,
  });

  await controller.load(true);
  assert.deepEqual(calls[0], ['/api/admin/update/check', { method: 'POST' }]);
  await controller.startUpdate();
  assert.deepEqual(calls[1], ['/api/admin/update', { method: 'POST' }]);
  assert.equal(values.get(UPDATE_TARGET_KEY), 'v1.1.0');
  assert.equal(scheduled.at(-1).delay, 1200);
  assert.equal(document.getElementById('update-status').textContent, 'Updating…');

  currentVersion = 'v1.1.0';
  await scheduled.at(-1).callback();
  assert.equal(values.has(UPDATE_TARGET_KEY), false);
  assert.equal(toasts.at(-1), 'Updated to v1.1.0');
  assert.equal(calls.at(-1)[0], '/api/admin/update');
  controller.stop();
});

test('停止更新轮询后旧请求不会再创建计时器', async () => {
  const document = createElementDocument(updateIDs);
  const scheduled = [];
  let resolveRequest;
  const controller = createUpdateController({
    document,
    api: () => new Promise(resolve => { resolveRequest = resolve; }),
    toast() {},
    storage: { getItem: () => null, setItem() {}, removeItem() {} },
    schedule(callback) {
      scheduled.push(callback);
      return scheduled.length;
    },
    cancel() {},
    now: () => 1_000,
  });

  controller.poll('v2.0.0');
  const oldRun = scheduled.shift()();
  controller.stop();
  resolveRequest({ current_version: 'v1.0.0', latest_version: 'v2.0.0', update_available: true });
  await oldRun;
  assert.equal(scheduled.length, 0);
});

test('新的更新轮询会作废旧轮询请求', async () => {
  const document = createElementDocument(updateIDs);
  const scheduled = [];
  const resolvers = [];
  const controller = createUpdateController({
    document,
    api: () => new Promise(resolve => { resolvers.push(resolve); }),
    toast() {},
    storage: { getItem: () => null, setItem() {}, removeItem() {} },
    schedule(callback) {
      scheduled.push(callback);
      return scheduled.length;
    },
    cancel() {},
    now: () => 1_000,
  });

  controller.poll('v2.0.0');
  const oldRun = scheduled.shift()();
  controller.poll('v2.0.0');
  assert.equal(scheduled.length, 1);
  resolvers[0]({ current_version: 'v1.0.0', latest_version: 'v2.0.0', update_available: true });
  await oldRun;
  assert.equal(scheduled.length, 1, 'old poll must not add a second timer');
  controller.stop();
});
