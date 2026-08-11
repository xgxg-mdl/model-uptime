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

console.log('status page bucket regression checks passed');
