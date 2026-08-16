import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  createMutationQueue,
  createEditorSessionState,
  selectEditorTrigger,
  setEditorSavePending,
} from '../../internal/httpserver/web/assets/scripts/admin/services.js';
import {
  revealPanel,
  setButtonPending,
} from '../../internal/httpserver/web/assets/scripts/admin/shared.js';
import { FakeDocument } from './helpers/fake-dom.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const html = fs.readFileSync(path.join(root, 'internal/httpserver/web/admin/index.html'), 'utf8');

test('服务编辑器在列表之后原地展开且 ID 唯一', () => {
  const servicePanelStart = html.indexOf('<h2>monitor services</h2>');
  const serviceListStart = html.indexOf('id="svc-list"', servicePanelStart);
  const editorStart = html.indexOf('id="editor"', servicePanelStart);
  const bulkEditorStart = html.indexOf('id="bulk-editor"', servicePanelStart);

  assert.ok(servicePanelStart >= 0 && serviceListStart >= 0 && editorStart >= 0 && bulkEditorStart >= 0);
  assert.ok(editorStart > serviceListStart && editorStart < bulkEditorStart);
  assert.match(html, /class="service-editor panel-reveal hidden" id="editor"/);
  assert.match(html, /role="region" aria-labelledby="editor-title"/);
  assert.doesNotMatch(html, /<section class="panel hidden" id="editor">/);

  for (const id of ['editor', 'svc-form', 'f-id-input', 'f-name', 'save-btn']) {
    assert.equal(html.split(`id="${id}"`).length, 2, `服务编辑器 ID 不是唯一值: ${id}`);
  }
});

test('编辑会话拒绝重复保存', () => {
  const state = createEditorSessionState();
  const version = state.open('service-1');
  const firstSave = state.beginSave();

  assert.deepEqual(firstSave, { version, serviceID: 'service-1' });
  assert.equal(state.saving, true);
  assert.equal(state.beginSave(), null);

  state.finishSave(version);
  assert.equal(state.saving, false);
  assert.deepEqual(state.beginSave(), { version, serviceID: 'service-1' });
});

test('旧保存完成后不能关闭或锁定新编辑草稿', () => {
  const state = createEditorSessionState();
  state.open('service-1');
  const staleSave = state.beginSave();
  state.close();
  const currentVersion = state.open('service-2');

  assert.equal(state.isCurrent(staleSave.version), false);
  assert.equal(state.editingID, 'service-2');
  assert.equal(state.saving, false);

  state.finishSave(staleSave.version);
  assert.equal(state.editingID, 'service-2');
  assert.deepEqual(state.beginSave(), { version: currentVersion, serviceID: 'service-2' });
});

test('旧保存的 finally 不能恢复新保存的按钮状态', () => {
  const document = new FakeDocument();
  const button = document.createElement('button');
  const state = createEditorSessionState();

  state.open('service-1');
  const staleSave = state.beginSave();
  assert.equal(setEditorSavePending(button, state, staleSave.version, true), true);

  const currentVersion = state.open('service-2');
  setEditorSavePending(button, state, currentVersion, false);
  const currentSave = state.beginSave();
  setEditorSavePending(button, state, currentSave.version, true);

  state.finishSave(staleSave.version);
  assert.equal(setEditorSavePending(button, state, staleSave.version, false), false);
  assert.equal(button.disabled, true);
  assert.equal(button.textContent, 'saving…');
  assert.equal(button.getAttribute('aria-busy'), 'true');

  state.finishSave(currentSave.version);
  assert.equal(setEditorSavePending(button, state, currentSave.version, false), true);
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, 'save');
  assert.equal(button.getAttribute('aria-busy'), 'false');
});

test('编辑器展开遵守 reduced-motion 设置', () => {
  const document = new FakeDocument();
  const editor = document.createElement('div');
  const input = document.createElement('input');
  editor.className = 'hidden';

  revealPanel(editor, { matchMedia: () => ({ matches: false }) }, 'nearest', input);
  assert.equal(editor.classList.contains('hidden'), false);
  assert.deepEqual(editor.lastScroll, { behavior: 'smooth', block: 'nearest' });
  assert.equal(document.activeElement, input);

  editor.classList.add('hidden');
  revealPanel(editor, { matchMedia: () => ({ matches: true }) }, 'start');
  assert.deepEqual(editor.lastScroll, { behavior: 'auto', block: 'start' });
});

test('异步按钮会公布忙碌状态并恢复原标签和禁用状态', () => {
  const document = new FakeDocument();
  const button = document.createElement('button');
  button.textContent = 'save';

  setButtonPending(button, true, 'saving…');
  assert.equal(button.disabled, true);
  assert.equal(button.textContent, 'saving…');
  assert.equal(button.getAttribute('aria-busy'), 'true');

  setButtonPending(button, false);
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, 'save');
  assert.equal(button.getAttribute('aria-busy'), 'false');
});

test('服务 mutation 按请求到达顺序串行化', async () => {
  const enqueue = createMutationQueue();
  const order = [];
  let releaseFirst;
  const first = enqueue(async () => {
    order.push('first-start');
    await new Promise(resolve => { releaseFirst = resolve; });
    order.push('first-end');
  });
  const second = enqueue(async () => { order.push('second'); });

  await Promise.resolve();
  assert.deepEqual(order, ['first-start']);
  releaseFirst();
  await Promise.all([first, second]);
  assert.deepEqual(order, ['first-start', 'first-end', 'second']);
});

test('响应式切换后焦点恢复到当前可见的编辑按钮', () => {
  const tableButton = { dataset: { id: 'service-1' } };
  const listButton = { dataset: { id: 'service-1' } };

  assert.equal(selectEditorTrigger([tableButton], [listButton], 'service-1', 960), tableButton);
  assert.equal(selectEditorTrigger([tableButton], [listButton], 'service-1', 959), listButton);
  assert.equal(selectEditorTrigger([tableButton], [listButton], 'missing', 320), null);
});
