import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { fillPageForm, readPageForm } from '../../internal/httpserver/web/assets/scripts/admin/page-settings.js';
import {
  DEFAULT_TELEGRAM_LANGUAGE,
  normalizeTelegramSubscription,
  normalizeTelegramTemplates,
  sendTelegramTest,
} from '../../internal/httpserver/web/assets/scripts/admin/telegram.js';
import { createElementDocument } from './helpers/fake-dom.js';

const root = fileURLToPath(new URL('../..', import.meta.url));
const html = fs.readFileSync(path.join(root, 'internal/httpserver/web/admin/index.html'), 'utf8');
const telegramSource = fs.readFileSync(
  path.join(root, 'internal/httpserver/web/assets/scripts/admin/telegram.js'),
  'utf8',
);

test('Telegram 模板以服务端响应为唯一默认来源', () => {
  const templates = normalizeTelegramTemplates({
    zh: '服务端中文模板 {{.TodayUptimePct}}',
    en: 'Server English template {{.OutageDurationSec}}',
  });
  assert.deepEqual(templates, {
    'zh-CN': '服务端中文模板 {{.TodayUptimePct}}',
    'en-US': 'Server English template {{.OutageDurationSec}}',
  });
  assert.deepEqual(
    normalizeTelegramTemplates({
      'zh-CN': 'legacy zh',
      'en-US': 'legacy en',
    }),
    {
      'zh-CN': 'legacy zh',
      'en-US': 'legacy en',
    },
  );
  assert.equal(DEFAULT_TELEGRAM_LANGUAGE, 'zh-CN');
  assert.doesNotMatch(telegramSource, /TodayUptimePct|OutageDurationSec|Model status update/);
});

test('新订阅使用中文服务端模板并复制 service_ids', () => {
  const templates = normalizeTelegramTemplates({
    zh: 'zh template',
    en: 'en template',
  });
  const serviceIDs = ['s1'];
  const subscription = normalizeTelegramSubscription(
    {
      id: 'ops',
      name: 'Operations',
      chat_id: 123,
      service_ids: serviceIDs,
    },
    templates,
  );

  serviceIDs.push('s2');
  assert.equal(subscription.language, 'zh-CN');
  assert.equal(subscription.template, 'zh template');
  assert.equal(subscription.chat_id, '123');
  assert.deepEqual(subscription.service_ids, ['s1']);
});

test('Telegram 测试在列表和编辑器中保留成功或失败反馈', async () => {
  const rowResult = {
    className: 'subscription-test-result feedback-in hidden',
    textContent: '',
  };
  const editorResult = {
    id: 'tg-test-result',
    className: 'test-result feedback-in hidden',
    textContent: '',
  };
  const calls = [];
  const toasts = [];
  let savedOptions;

  const success = await sendTelegramTest({
    subscription: { id: 'ops', name: 'Operations' },
    results: [rowResult, editorResult],
    save: async options => {
      savedOptions = options;
    },
    api: async (...args) => {
      calls.push(args);
    },
    toast: message => toasts.push(message),
  });
  assert.equal(success, true);
  assert.deepEqual(savedOptions, { quiet: true });
  assert.equal(calls[0][0], '/api/admin/telegram/test');
  assert.deepEqual(JSON.parse(calls[0][1].body), { subscription_id: 'ops' });
  for (const result of [rowResult, editorResult]) {
    assert.equal(result.textContent, 'test message sent');
    assert.match(result.className, /\bok\b/);
    assert.doesNotMatch(result.className, /\bhidden\b/);
  }
  assert.equal(toasts.at(-1), 'Test message sent for Operations');

  const failure = await sendTelegramTest({
    subscription: { id: 'ops', name: 'Operations' },
    results: [rowResult],
    save: async () => {},
    api: async () => {
      throw new Error('Telegram API returned: chat not found');
    },
    toast: message => toasts.push(message),
  });
  assert.equal(failure, false);
  assert.equal(rowResult.textContent, 'Telegram API returned: chat not found');
  assert.match(rowResult.className, /\bbad\b/);
});

test('页面公开地址参与配置读取和服务端回填', () => {
  assert.match(html, /id="p-public-url"/);
  assert.match(html, /<option value="zh-CN">简体中文<\/option>/);
  const ids = [
    'p-title',
    'p-subtitle',
    'p-comment',
    'p-public-url',
    'p-history',
    'p-refresh',
    'p-uptime',
    'p-samples',
    'p-latency',
    'p-avload',
  ];
  const document = createElementDocument(ids);
  fillPageForm(document, {
    title: 'Status',
    subtitle: 'Monitor',
    probe_comment: 'Probing',
    public_url: 'https://status.example.com/',
    history_len: 30,
    refresh_sec: 9,
    show_uptime: true,
    show_samples: false,
    show_latency: true,
    show_avg_load: false,
  });

  assert.equal(readPageForm(document).public_url, 'https://status.example.com/');
});
