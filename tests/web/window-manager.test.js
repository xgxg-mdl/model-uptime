import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  clampWindowPosition,
  createWindowManager,
  nextWindowMode,
} from '../../internal/httpserver/web/assets/scripts/window-manager.js';
import { FakeDocument } from './helpers/fake-dom.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const webRoot = path.join(root, 'internal/httpserver/web');
const foundationCSS = fs.readFileSync(path.join(webRoot, 'assets/styles/foundation.css'), 'utf8');
const pageSources = [
  ['status', fs.readFileSync(path.join(webRoot, 'index.html'), 'utf8')],
  ['heatmap', fs.readFileSync(path.join(webRoot, 'heatmap/index.html'), 'utf8')],
  ['admin', fs.readFileSync(path.join(webRoot, 'admin/index.html'), 'utf8')],
];

function createWindow(document, { id, label, main = false, popup = false, hidden = false }) {
  const element = document.registerElement(
    id,
    'section',
    `mac-window${popup ? ' popup-window' : ''}${hidden ? ' hidden' : ''}`,
  );
  element.setAttribute('data-window-id', id);
  element.setAttribute('data-window-mode', 'normal');
  element.setAttribute('data-window-dock-label', label);
  if (main) element.setAttribute('data-window-main', '');
  if (popup) {
    element.setAttribute('data-window-popup', '');
    element.setAttribute('aria-modal', 'true');
  }

  const titlebar = document.createElement('div');
  titlebar.setAttribute('data-window-titlebar', '');
  const controls = document.createElement('div');
  controls.className = 'window-controls';
  for (const action of ['close', 'minimize', 'maximize']) {
    const control = document.createElement('button');
    control.className = `window-control ${action}`;
    control.setAttribute('data-window-action', action);
    controls.append(control);
  }
  titlebar.append(controls);

  const input = document.createElement('input');
  input.setAttribute('data-window-initial-focus', '');
  element.append(titlebar, input);
  return { element, input, controls };
}

function createWindowHarness() {
  const document = new FakeDocument();
  document.documentElement = { clientWidth: 1200, clientHeight: 800 };
  const dock = document.registerElement('window-dock');
  const layer = document.registerElement('window-layer');
  const main = createWindow(document, { id: 'main', label: 'status', main: true });
  const popup = createWindow(document, { id: 'popup', label: 'service editor', popup: true, hidden: true });
  document.querySelectorAll = selector => (selector === '[data-window-id]' ? [main.element, popup.element] : []);

  const listeners = new Map();
  const viewportListeners = new Map();
  const window = {
    innerWidth: 1200,
    innerHeight: 800,
    matchMedia: () => ({ matches: true }),
    addEventListener(type, listener) {
      listeners.set(type, listener);
    },
    visualViewport: {
      width: 1200,
      height: 800,
      offsetLeft: 0,
      offsetTop: 0,
      addEventListener(type, listener) {
        viewportListeners.set(type, listener);
      },
    },
  };
  return { document, dock, layer, main, popup, window, listeners, viewportListeners };
}

function click(element) {
  element.dispatchEvent({
    type: 'click',
    stopPropagation() {},
  });
}

test('窗口状态按 macOS 控制按钮语义切换', () => {
  assert.equal(nextWindowMode('normal', 'minimize'), 'minimized');
  assert.equal(nextWindowMode('minimized', 'minimize'), 'normal');
  assert.equal(nextWindowMode('normal', 'maximize'), 'maximized');
  assert.equal(nextWindowMode('maximized', 'maximize'), 'normal');
  assert.equal(nextWindowMode('minimized', 'maximize'), 'maximized');
});

test('拖动窗口始终收进桌面和移动视口', () => {
  assert.deepEqual(
    clampWindowPosition({ x: -100, y: -50, width: 200, height: 120, viewportWidth: 390, viewportHeight: 844 }),
    { x: 8, y: 8 },
  );
  assert.deepEqual(
    clampWindowPosition({ x: 500, y: 900, width: 200, height: 120, viewportWidth: 390, viewportHeight: 844 }),
    { x: 182, y: 716 },
  );
  assert.deepEqual(
    clampWindowPosition({ x: 80, y: 80, width: 500, height: 700, viewportWidth: 320, viewportHeight: 568 }),
    { x: 8, y: 8 },
  );
  assert.deepEqual(
    clampWindowPosition({
      x: 0,
      y: 0,
      width: 300,
      height: 240,
      viewportWidth: 390,
      viewportHeight: 500,
      viewportLeft: 20,
      viewportTop: 100,
    }),
    { x: 28, y: 108 },
  );
});

test('最大化、最小化和恢复同步窗口状态并保留位置', () => {
  const harness = createWindowHarness();
  const windows = createWindowManager({ document: harness.document, window: harness.window });
  harness.main.input.focus();

  windows.open('popup');
  assert.equal(harness.document.activeElement, harness.popup.input);
  assert.equal(harness.popup.element.getAttribute('aria-hidden'), 'false');
  harness.popup.element.style.left = '120px';
  harness.popup.element.style.top = '90px';

  const maximize = harness.popup.controls.querySelector('[data-window-action="maximize"]');
  click(maximize);
  assert.equal(harness.popup.element.dataset.windowMode, 'maximized');
  assert.ok(maximize.classList.contains('is-restore'));
  assert.equal(maximize.getAttribute('aria-label'), 'Restore service editor window');
  assert.equal(maximize.getAttribute('aria-pressed'), 'true');

  click(harness.popup.controls.querySelector('[data-window-action="minimize"]'));
  assert.equal(harness.popup.element.dataset.windowMode, 'minimized');
  assert.ok(harness.popup.element.classList.contains('hidden'));
  assert.equal(harness.popup.element.getAttribute('aria-hidden'), 'true');
  const dockItem = harness.dock.querySelector('[data-window-restore="popup"]');
  assert.ok(dockItem.classList.contains('is-minimized'));
  assert.ok(dockItem.querySelector('.window-dock-preview'));
  assert.equal(dockItem.getAttribute('aria-label'), 'Restore service editor window');

  click(dockItem);
  assert.equal(harness.popup.element.dataset.windowMode, 'maximized');
  assert.equal(harness.popup.element.style.left, '120px');
  assert.equal(harness.popup.element.style.top, '90px');
  click(maximize);
  assert.equal(harness.popup.element.dataset.windowMode, 'normal');
  assert.equal(harness.popup.element.style.left, '120px');
});

test('关闭与最小化生成不同的 Dock 状态', () => {
  const harness = createWindowHarness();
  const windows = createWindowManager({ document: harness.document, window: harness.window });

  click(harness.main.controls.querySelector('[data-window-action="close"]'));
  assert.equal(harness.main.element.dataset.windowMode, 'closed');
  const launcher = harness.dock.querySelector('[data-window-restore="main"]');
  assert.ok(launcher.classList.contains('is-closed'));
  assert.ok(launcher.querySelector('.window-dock-app-mark'));
  assert.equal(launcher.getAttribute('aria-label'), 'Open status window');

  click(launcher);
  assert.equal(harness.main.element.dataset.windowMode, 'normal');
  assert.equal(harness.dock.querySelector('[data-window-restore="main"]'), null);
  windows.toggleMode('main', 'minimize');
  assert.ok(harness.dock.querySelector('[data-window-restore="main"]').classList.contains('is-minimized'));
});

test('弹窗接管并恢复键盘焦点，Escape 关闭当前弹窗', () => {
  const harness = createWindowHarness();
  const windows = createWindowManager({ document: harness.document, window: harness.window });
  harness.main.input.focus();
  windows.open('popup');
  assert.equal(harness.document.activeElement, harness.popup.input);

  let tabPrevented = false;
  harness.document.dispatchEvent({
    type: 'keydown',
    key: 'Tab',
    preventDefault() {
      tabPrevented = true;
    },
  });
  assert.equal(tabPrevented, true);
  assert.equal(harness.document.activeElement, harness.popup.controls.querySelector('[data-window-action="close"]'));

  harness.main.input.focus();
  assert.ok(harness.main.element.classList.contains('is-active'));
  harness.popup.input.focus();
  assert.ok(harness.popup.element.classList.contains('is-active'));

  windows.setCloseHandler('popup', () => windows.close('popup'));
  harness.document.dispatchEvent({ type: 'keydown', key: 'Escape', preventDefault() {} });
  assert.ok(harness.popup.element.classList.contains('hidden'));
  assert.equal(harness.document.activeElement, harness.main.input);
});

test('visualViewport 变化会把未拖动弹窗移出软键盘区域', () => {
  const harness = createWindowHarness();
  const windows = createWindowManager({ document: harness.document, window: harness.window });
  windows.open('popup');
  harness.popup.element.rect = { left: 100, top: 600, width: 300, height: 240 };
  Object.assign(harness.window.visualViewport, { width: 390, height: 500, offsetLeft: 20, offsetTop: 100 });

  harness.viewportListeners.get('resize')();
  assert.equal(harness.popup.element.style.left, '100px');
  assert.equal(harness.popup.element.style.top, '352px');
  assert.equal(harness.popup.element.style.transform, 'none');
});

test('状态、热力图和管理页接入同一套窗口控制结构', () => {
  for (const [page, source] of pageSources) {
    assert.match(source, /data-window-main/, `${page} 缺少主窗口声明`);
    for (const action of ['close', 'minimize', 'maximize']) {
      assert.match(source, new RegExp(`data-window-action="${action}"`), `${page} 缺少 ${action} 控制`);
    }
    assert.match(source, /id="window-dock"/, `${page} 缺少窗口恢复区`);
  }

  for (const marker of [
    '--window-control-inactive:',
    '--window-control-spacing: 20px;',
    '--window-control-size: 12px;',
    '--window-control-symbol-size: 6px;',
    '.mac-window.is-active .window-control.close::after',
    '.window-controls:hover .window-control.close::after',
    '.window-control.maximize.is-restore::before',
    '@keyframes window-open',
    '.mac-window.is-opening:not(.popup-window)',
    '.popup-window.is-maximized',
    '.window-dock',
    '.window-dock-preview',
  ]) {
    assert.ok(foundationCSS.includes(marker), `共享基础层缺少窗口规则: ${marker}`);
  }
  assert.match(foundationCSS, /\.window-control\s*{[^}]*width:\s*var\(--window-control-spacing\);/s);
  assert.doesNotMatch(foundationCSS, /@media \(max-width: 640px\)[\s\S]*?\.window-control\s*{[^}]*width:/);
  assert.match(foundationCSS, /\.window-control::after\s*{[^}]*width:\s*var\(--window-control-size\);/s);
  assert.match(foundationCSS, /\.window-control\.close::before\s*{[^}]*width:\s*var\(--window-control-symbol-size\);/s);
  assert.match(
    foundationCSS,
    /\.window-control\.maximize::before\s*{[^}]*width:\s*var\(--window-control-symbol-size\);/s,
  );
});
