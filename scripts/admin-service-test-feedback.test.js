const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'admin', 'index.html'), 'utf8');
if (!html.includes('data-service-test-status="${id}"')) {
  throw new Error('服务列表缺少行内测试结果区域');
}

const start = html.indexOf('function testService(id, result, button)');
const end = html.indexOf('\n// ---------- 编辑表单', start);
if (start < 0 || end < 0) throw new Error('testService 尚未接收可见结果节点');

const result = { hidden: true, className: 'service-test-result hidden', textContent: '', innerHTML: '' };
const button = { disabled: false };
let resolveAPI;
const context = {
  api: () => new Promise(resolve => { resolveAPI = resolve; }),
  esc: String,
  encodeURIComponent,
};
vm.createContext(context);
vm.runInContext(html.slice(start, end), context);

(async () => {
  const success = context.testService('service-1', result, button);
  if (!button.disabled || result.textContent !== 'probing…' || result.hidden) {
    throw new Error('列表测试没有立即显示加载状态');
  }
  resolveAPI({ ok: true, latency_ms: 12 });
  await success;
  if (button.disabled || !result.innerHTML.includes('OK') || !/\bok\b/.test(result.className)) {
    throw new Error('列表测试没有留下成功结果');
  }

  context.api = async () => { throw new Error('connection refused'); };
  await context.testService('service-1', result, button);
  if (button.disabled || result.textContent !== 'connection refused' || !/\bbad\b/.test(result.className)) {
    throw new Error('列表测试没有留下错误结果');
  }
  console.log('admin service test feedback regression check passed');
})().catch(error => {
  console.error(error.message);
  process.exitCode = 1;
});
