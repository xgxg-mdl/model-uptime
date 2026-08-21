import assert from 'node:assert/strict';
import test from 'node:test';

import { showServiceTestResult } from '../../internal/httpserver/web/assets/scripts/admin/services.js';

function resultElement(className = 'service-test-result feedback-in hidden') {
  return { hidden: true, className, textContent: '', innerHTML: '' };
}

test('服务测试立即显示进度并保留成功结果', async () => {
  const result = resultElement();
  const button = { disabled: false };
  let resolveAPI;
  const api = () =>
    new Promise(resolve => {
      resolveAPI = resolve;
    });

  const request = showServiceTestResult({
    api,
    uid: 'service-1',
    result,
    button,
  });
  assert.equal(button.disabled, true);
  assert.equal(result.hidden, false);
  assert.equal(result.textContent, 'probing…');

  resolveAPI({ ok: true, latency_ms: 12 });
  const response = await request;
  assert.equal(response.ok, true);
  assert.equal(button.disabled, false);
  assert.match(result.className, /\bok\b/);
  assert.match(result.innerHTML, /✓ OK.*12ms/);
});

test('服务测试转义探测错误并保留失败反馈', async () => {
  const result = resultElement();
  const button = { disabled: false };

  await showServiceTestResult({
    api: async () => ({
      ok: false,
      latency_ms: 25,
      error: '<img src=x onerror=alert(1)>',
    }),
    uid: 'service-1',
    result,
    button,
  });
  assert.match(result.className, /\bbad\b/);
  assert.match(result.innerHTML, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.doesNotMatch(result.innerHTML, /<img/);

  await showServiceTestResult({
    api: async () => {
      throw new Error('connection refused');
    },
    uid: 'service-1',
    result,
    button,
  });
  assert.equal(result.textContent, 'connection refused');
  assert.match(result.className, /\bbad\b/);
  assert.equal(button.disabled, false);
});
