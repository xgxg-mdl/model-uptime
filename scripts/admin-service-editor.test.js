const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '..', 'internal', 'api', 'web', 'admin', 'index.html'), 'utf8');

const servicePanelStart = html.indexOf('<h2>monitor services</h2>');
const serviceListStart = html.indexOf('id="svc-list"', servicePanelStart);
const bulkEditorStart = html.indexOf('id="bulk-editor"', servicePanelStart);
const editorStart = html.indexOf('id="editor"', servicePanelStart);
if (servicePanelStart < 0 || serviceListStart < 0 || bulkEditorStart < 0 || editorStart < 0) {
  throw new Error('服务面板或编辑器标记缺失');
}
if (!(editorStart > serviceListStart && editorStart < bulkEditorStart)) {
  throw new Error('服务编辑器没有在桌面表格和移动列表之后原地展开');
}
for (const marker of [
  'class="service-editor panel-reveal hidden" id="editor"',
  'role="region" aria-labelledby="editor-title"',
  "document.getElementById('svc-form').addEventListener('submit', saveService);",
]) {
  if (!html.includes(marker)) throw new Error(`服务编辑器缺少原地生命周期标记: ${marker}`);
}
if (html.includes('<section class="panel hidden" id="editor">')) {
  throw new Error('服务编辑器仍是独立面板');
}
for (const id of ['editor', 'svc-form', 'f-id-input', 'f-name', 'save-btn']) {
  if (html.split(`id="${id}"`).length !== 2) throw new Error(`服务编辑器 ID 不是唯一值: ${id}`);
}
if (!html.includes('if (editingID && !adminServices.some(service => service.id === editingID)) closeEditor();')) {
  throw new Error('服务刷新后没有关闭已失效的编辑目标');
}

function extractFunction(name) {
  let start = html.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`未找到函数 ${name}`);
  if (html.slice(start - 6, start) === 'async ') start -= 6;
  const bodyStart = html.indexOf('{', start);
  let depth = 0;
  for (let i = bodyStart; i < html.length; i++) {
    if (html[i] === '{') depth++;
    else if (html[i] === '}' && --depth === 0) return html.slice(start, i + 1);
  }
  throw new Error(`函数 ${name} 没有结束`);
}

function makeElement() {
  const classes = new Set(['hidden']);
  return {
    value: '', checked: false, disabled: false, hidden: false, textContent: '', className: '',
    classList: {
      add(...names) { names.forEach(name => classes.add(name)); },
      remove(...names) { names.forEach(name => classes.delete(name)); },
      toggle(name, force) {
        const enabled = force === undefined ? !classes.has(name) : force;
        if (enabled) classes.add(name); else classes.delete(name);
      },
      contains(name) { return classes.has(name); },
    },
    scrollIntoView(options) { this.lastScroll = options; },
  };
}

const ids = [
  'editor', 'editor-title', 'f-id-input', 'f-name', 'f-provider', 'f-protocol', 'f-model',
  'f-base', 'f-key', 'f-path', 'f-interval', 'f-timeout', 'f-enabled', 'f-stream',
  'f-method', 'f-expect', 'f-headers', 'f-body', 'test-result', 'save-btn',
];
const elements = Object.fromEntries(ids.map(id => [id, makeElement()]));
let apiResolve;
let loadResolve;
let toastMessage = '';
let loadCalls = 0;
let apiCalls = 0;
const apiRequests = [];
const context = {
  editingID: null,
  editorSession: 0,
  savingEditorSession: null,
  window: { matchMedia: () => ({ matches: false }) },
  document: {
    getElementById(id) {
      if (!elements[id]) throw new Error(`意外的元素查询: ${id}`);
      return elements[id];
    },
  },
  showHttpFields() {},
  collectService: () => ({ id: '', name: 'Example' }),
  api: (...args) => {
    apiCalls++;
    apiRequests.push(args);
    return new Promise(resolve => { apiResolve = resolve; });
  },
  loadServices: () => {
    loadCalls++;
    return new Promise(resolve => { loadResolve = resolve; });
  },
  loadTelegram: async () => {},
  toast: message => { toastMessage = message; },
  confirm: () => true,
  encodeURIComponent,
};
vm.createContext(context);
for (const name of [
  'prefersReducedMotion', 'revealPanel', 'startEditorSession', 'closeEditor',
  'openEditor', 'openNew', 'saveService', 'deleteService',
]) {
  vm.runInContext(extractFunction(name), context);
}

(async () => {
  const service = {
    id: 'service-1', name: 'Example', provider: 'OpenAI', protocol: 'chat', model: 'gpt-5',
    base_url: 'https://example.com', interval_sec: 60, timeout_sec: 15, enabled: true,
  };
  context.openEditor(service);
  if (context.editingID !== service.id || elements.editor.classList.contains('hidden')) {
    throw new Error('编辑操作没有原地打开对应服务');
  }
  if (elements.editor.lastScroll?.behavior !== 'smooth' || elements.editor.lastScroll?.block !== 'nearest') {
    throw new Error('原地编辑器没有保留列表上下文');
  }

  context.closeEditor();
  if (context.editingID !== null || !elements.editor.classList.contains('hidden')) {
    throw new Error('取消编辑没有清理编辑状态');
  }

  context.window.matchMedia = () => ({ matches: true });
  context.revealPanel(elements.editor);
  if (elements.editor.lastScroll?.behavior !== 'auto') {
    throw new Error('减少动态效果时仍使用平滑滚动');
  }

  context.window.matchMedia = () => ({ matches: false });
  context.openNew();
  const submit = context.saveService({ preventDefault() {} });
  await context.saveService({ preventDefault() {} });
  if (!elements['save-btn'].disabled) throw new Error('保存期间没有禁用提交按钮');
  if (apiCalls !== 1) throw new Error('重复提交发送了多次创建请求');
  if (apiRequests[0][0] !== '/api/admin/services' || apiRequests[0][1].method !== 'POST') {
    throw new Error('新建服务没有使用 POST');
  }
  apiResolve({ service: { id: 'created-service' } });
  await new Promise(resolve => setImmediate(resolve));
  let settled = false;
  submit.then(() => { settled = true; });
  await new Promise(resolve => setImmediate(resolve));
  if (settled || loadCalls !== 1) throw new Error('保存成功后没有等待服务列表刷新');
  loadResolve();
  await submit;
  if (!elements.editor.classList.contains('hidden') || elements['save-btn'].disabled || toastMessage !== '已创建') {
    throw new Error('保存成功后没有关闭编辑器并恢复按钮');
  }

  let raceAPIResolve;
  let raceLoadResolve;
  context.openEditor(service);
  context.api = (...args) => {
    apiRequests.push(args);
    return new Promise(resolve => { raceAPIResolve = resolve; });
  };
  context.loadServices = () => new Promise(resolve => { raceLoadResolve = resolve; });
  const raceSave = context.saveService({ preventDefault() {} });
  context.closeEditor();
  const nextService = { ...service, id: 'service-2', name: 'Next draft' };
  context.openEditor(nextService);
  if (elements['save-btn'].disabled) throw new Error('新编辑会话被旧保存请求锁定');
  raceAPIResolve({});
  await new Promise(resolve => setImmediate(resolve));
  if (apiRequests.at(-1)[0] !== '/api/admin/services/service-1' || apiRequests.at(-1)[1].method !== 'PUT') {
    throw new Error('编辑服务没有使用对应 ID 的 PUT');
  }
  raceLoadResolve();
  await raceSave;
  if (context.editingID !== nextService.id || elements.editor.classList.contains('hidden') || elements['save-btn'].disabled) {
    throw new Error('旧保存请求关闭或锁定了后来打开的编辑会话');
  }

  context.openNew();
  context.api = async () => { throw new Error('save failed'); };
  await context.saveService({ preventDefault() {} });
  if (elements.editor.classList.contains('hidden') || elements['save-btn'].disabled || toastMessage !== 'save failed') {
    throw new Error('保存失败时没有保留编辑草稿');
  }

  context.openEditor(service);
  context.api = async () => ({});
  context.loadServices = async () => {};
  await context.deleteService(service.id);
  if (context.editingID !== null || !elements.editor.classList.contains('hidden')) {
    throw new Error('删除当前服务后没有关闭失效编辑器');
  }

  console.log('admin inline service editor regression checks passed');
})().catch(error => {
  console.error(error.message);
  process.exitCode = 1;
});
