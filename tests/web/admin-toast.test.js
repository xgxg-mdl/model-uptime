import assert from 'node:assert/strict';
import test from 'node:test';

import { createToast } from '../../internal/httpserver/web/assets/scripts/admin/shared.js';

function createToastElement() {
  const classes = new Set(['toast']);
  const attributes = new Map();
  return {
    attributes,
    classes,
    textContent: '',
    classList: {
      add: (...names) => names.forEach(name => classes.add(name)),
      remove: (...names) => names.forEach(name => classes.delete(name)),
    },
    setAttribute: (name, value) => attributes.set(name, value),
  };
}

test('toast 切换状态语义并替换正在等待的关闭计时器', () => {
  const element = createToastElement();
  const timers = [];
  const canceled = [];
  const toast = createToast({
    document: { getElementById: () => element },
    schedule: (callback, delay) => {
      timers.push({ callback, delay });
      return timers.length;
    },
    cancel: timer => canceled.push(timer),
  });

  toast('Configuration saved', 'success');
  assert.equal(element.textContent, 'Configuration saved');
  assert.equal(element.classes.has('success'), true);
  assert.equal(element.classes.has('show'), true);
  assert.equal(element.attributes.get('role'), 'status');
  assert.equal(element.attributes.get('aria-live'), 'polite');
  assert.equal(element.attributes.get('aria-hidden'), 'false');
  assert.equal(timers[0].delay, 2800);

  toast('Connection failed', 'error');
  assert.deepEqual(canceled, [1]);
  assert.equal(element.classes.has('success'), false);
  assert.equal(element.classes.has('error'), true);
  assert.equal(element.attributes.get('role'), 'alert');
  assert.equal(element.attributes.get('aria-live'), 'assertive');
  assert.equal(timers[1].delay, 4200);

  timers[1].callback();
  assert.equal(element.classes.has('show'), false);
  assert.equal(element.attributes.get('aria-hidden'), 'true');
});

test('toast 对未知状态回退为 info', () => {
  const element = createToastElement();
  const toast = createToast({
    document: { getElementById: () => element },
    schedule: () => 1,
    cancel: () => {},
  });

  toast('Queued', 'unknown');
  assert.equal(element.classes.has('info'), true);
  assert.equal(element.attributes.get('role'), 'status');
});
