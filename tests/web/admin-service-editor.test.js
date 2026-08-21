import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  compareServiceOrder,
  createEditorSessionState,
  renderServiceListItem,
  renderServiceTableRow,
} from '../../internal/httpserver/web/assets/scripts/admin/services.js';
import { revealPanel } from '../../internal/httpserver/web/assets/scripts/admin/shared.js';
import { FakeDocument } from './helpers/fake-dom.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const html = fs.readFileSync(path.join(root, 'internal/httpserver/web/admin/index.html'), 'utf8');

test('服务编辑器以独立窗口声明且 DOM ID 唯一', () => {
  const servicePanelStart = html.indexOf('<h2>monitor services</h2>');
  const serviceListStart = html.indexOf('id="svc-list"', servicePanelStart);
  const editorStart = html.indexOf('id="editor"', servicePanelStart);
  const bulkEditorStart = html.indexOf('id="bulk-editor"', servicePanelStart);

  assert.ok(servicePanelStart >= 0 && serviceListStart >= 0 && editorStart >= 0 && bulkEditorStart >= 0);
  assert.ok(editorStart > serviceListStart && editorStart < bulkEditorStart);
  assert.match(html, /class="service-editor subwindow mac-window popup-window hidden" id="editor"/);
  assert.match(html, /id="editor" data-window-id="editor" data-window-popup data-window-mode="normal"/);
  assert.match(html, /role="dialog" aria-modal="false" aria-labelledby="editor-title"/);
  assert.match(html, /<div class="window-layer" id="window-layer"><\/div>/);
  assert.doesNotMatch(html, /<section class="panel hidden" id="editor">/);

  for (const id of ['editor', 'svc-form', 'f-name', 'f-model', 'f-sort-order', 'save-btn']) {
    const matches = html.match(new RegExp(`(?:^|\\s)id="${id}"`, 'g')) || [];
    assert.equal(matches.length, 1, `服务编辑器 ID 不是唯一值: ${id}`);
  }

  assert.doesNotMatch(html, /id="f-id-input"/);
  assert.match(html, /<input id="f-model" required/);
  assert.doesNotMatch(html, /class="field llm">\s*<label>Model/);
});

test('服务列表按通知排序字段排列，缺失值放在末尾', () => {
  const services = [
    { uid: 'uid-missing', model: 'missing', name: 'Missing' },
    { uid: 'uid-later', model: 'later', name: 'Later', sort_order: 20 },
    { uid: 'uid-first', model: 'first', name: 'First', sort_order: 10 },
  ];
  services.sort(compareServiceOrder);
  assert.deepEqual(
    services.map(service => service.model),
    ['first', 'later', 'missing'],
  );
});

test('服务列表使用 uid 定位但不向用户展示', () => {
  const service = {
    uid: '01HIDDENUID',
    model: 'gpt-4o',
    name: 'GPT 4o',
    protocol: 'chat',
    enabled: true,
  };

  for (const markup of [renderServiceTableRow(service), renderServiceListItem(service)]) {
    assert.match(markup, /data-uid="01HIDDENUID"/);
    assert.doesNotMatch(markup, />#01HIDDENUID</);
    assert.match(markup, /gpt-4o/);
  }
});

test('编辑会话拒绝重复保存', () => {
  const state = createEditorSessionState();
  const version = state.open('service-1');
  const firstSave = state.beginSave();

  assert.deepEqual(firstSave, { version, serviceUID: 'service-1' });
  assert.equal(state.saving, true);
  assert.equal(state.beginSave(), null);

  state.finishSave(version);
  assert.equal(state.saving, false);
  assert.deepEqual(state.beginSave(), { version, serviceUID: 'service-1' });
});

test('旧保存完成后不能关闭或锁定新编辑草稿', () => {
  const state = createEditorSessionState();
  state.open('service-1');
  const staleSave = state.beginSave();
  state.close();
  const currentVersion = state.open('service-2');

  assert.equal(state.isCurrent(staleSave.version), false);
  assert.equal(state.editingUID, 'service-2');
  assert.equal(state.saving, false);

  state.finishSave(staleSave.version);
  assert.equal(state.editingUID, 'service-2');
  assert.deepEqual(state.beginSave(), {
    version: currentVersion,
    serviceUID: 'service-2',
  });
});

test('编辑器展开遵守 reduced-motion 设置', () => {
  const document = new FakeDocument();
  const editor = document.createElement('div');
  editor.className = 'hidden';

  revealPanel(editor, { matchMedia: () => ({ matches: false }) });
  assert.equal(editor.classList.contains('hidden'), false);
  assert.deepEqual(editor.lastScroll, { behavior: 'smooth', block: 'nearest' });

  editor.classList.add('hidden');
  revealPanel(editor, { matchMedia: () => ({ matches: true }) }, 'start');
  assert.deepEqual(editor.lastScroll, { behavior: 'auto', block: 'start' });
});
