const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const html = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'index.html'), 'utf8');
const source = html.match(/function projectHistorySlots\([\s\S]*?\n}\n\nfunction renderBarsHtml/);
if (!source) throw new Error('未找到 projectHistorySlots');
const context = {};
vm.createContext(context);
vm.runInContext(source[0].replace(/\n\nfunction renderBarsHtml$/, ''), context);

const project = context.projectHistorySlots;
const generatedAt = 1_000;
const intervalSec = 60;
const historyLen = 5;

const rightBoundary = project([{ ts: generatedAt, ok: true }], historyLen, intervalSec, generatedAt);
if (rightBoundary.at(-1)?.ts !== generatedAt) {
  throw new Error('generated_at 样本必须落入最后一个桶');
}

const future = project([{ ts: generatedAt + 1, ok: true }], historyLen, intervalSec, generatedAt);
if (future.some(Boolean)) {
  throw new Error('未来样本不能进入窗口');
}

const sameBucket = project([
  { ts: generatedAt - 5, ok: false },
  { ts: generatedAt - 1, ok: true },
], historyLen, intervalSec, generatedAt);
if (sameBucket.at(-1)?.ts !== generatedAt - 1) {
  throw new Error('同桶样本必须保留最新结果');
}

// 暂停空档：停用前样本留在左侧桶，停用期间没有样本的桶为 null，
// 重新启用后的样本落在当前桶，历史不丢失。
// windowStart = 1000 - 5*60 = 700；桶 0 覆盖 [700,760)，桶 4 覆盖 [940,1000]。
const pauseGap = project([
  { ts: 705, ok: true },
  { ts: 999, ok: true },
], historyLen, intervalSec, generatedAt);
if (pauseGap[0]?.ts !== 705) {
  throw new Error('停用前样本应保留在左侧桶');
}
if (pauseGap.at(-1)?.ts !== 999) {
  throw new Error('恢复后样本应落入当前桶');
}
if (pauseGap[1] !== null || pauseGap[2] !== null || pauseGap[3] !== null) {
  throw new Error('停用期间无样本的桶必须为 null（灰色空档）');
}

console.log('status page bucket regression checks passed');
