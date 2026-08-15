const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'admin', 'index.html'), 'utf8');
for (const snippet of ['id="p-public-url"', "p.public_url || ''", "public_url: document.getElementById('p-public-url').value.trim()"]) {
  if (!html.includes(snippet)) throw new Error('探针页公开地址没有完整接入管理页');
}
if (!html.includes('<option value="zh-CN">简体中文</option>') || !html.includes("const DEFAULT_TELEGRAM_LANGUAGE = 'zh-CN';")) {
  throw new Error('Telegram 新订阅没有默认使用中文');
}
if (!html.includes("'en-US': `<b>{{if and .DownModels .RecoveredModels}}⚠️ Model status update")) {
  throw new Error('Telegram 管理页缺少英文内置模板');
}
for (const field of ['.OutageDurationSec', '.TodayUpSec', '.TodayDownSec', '.TodayDownCount', '.TodayUptimePct']) {
  if (!html.includes(field)) throw new Error(`Telegram 管理页缺少统计模板变量 ${field}`);
}
if (html.includes('异常原因：{{if .Error}}') || html.includes('Error: {{if .Error}}')) {
  throw new Error('Telegram 内置模板不应返回探测错误详情');
}
const start = html.indexOf('async function testTelegramSubscription(index)');
const end = html.indexOf('\nfunction deleteTelegramSubscription', start);
if (start < 0 || end < 0) throw new Error('未找到 testTelegramSubscription');

const hiddenEditorResult = { className: 'test-result hidden', textContent: '' };
const visibleRowResult = { className: 'subscription-test-result hidden', textContent: '' };
const context = {
  telegramConfig: {
    subscriptions: [{ id: 'ops', name: 'Operations', chat_id: '1', service_ids: ['s1'], template: 'test' }],
  },
  editingTelegramIndex: null,
  document: {
    getElementById(id) {
      if (id === 'tg-test-result') return hiddenEditorResult;
      throw new Error(`意外的元素查询: ${id}`);
    },
    querySelector(selector) {
      if (selector === '[data-tg-test-status="0"]') return visibleRowResult;
      return null;
    },
  },
  saveTelegramConfig: async () => {},
  api: async () => ({ ok: true }),
  toast: () => {},
};
vm.createContext(context);
vm.runInContext(html.slice(start, end), context);

(async () => {
  await context.testTelegramSubscription(0);
  if (visibleRowResult.textContent !== 'test message sent' || /\bhidden\b/.test(visibleRowResult.className)) {
    throw new Error('订阅列表行没有留下可见的测试结果');
  }
  context.api = async () => { throw new Error('Telegram API returned: chat not found'); };
  await context.testTelegramSubscription(0);
  if (visibleRowResult.textContent !== 'Telegram API returned: chat not found' || !/\bbad\b/.test(visibleRowResult.className)) {
    throw new Error('订阅列表行没有持久显示 Telegram 错误');
  }
  console.log('admin Telegram test feedback regression check passed');
})().catch(error => {
  console.error(error.message);
  process.exitCode = 1;
});
