class FakeClassList {
  constructor(element) {
    this.element = element;
  }

  values() {
    return this.element.className.split(/\s+/).filter(Boolean);
  }

  write(values) {
    this.element.className = [...new Set(values)].join(' ');
  }

  add(...names) {
    this.write([...this.values(), ...names]);
  }

  remove(...names) {
    const removed = new Set(names);
    this.write(this.values().filter(name => !removed.has(name)));
  }

  toggle(name, force) {
    const enabled = force === undefined ? !this.contains(name) : Boolean(force);
    if (enabled) this.add(name);
    else this.remove(name);
    return enabled;
  }

  contains(name) {
    return this.values().includes(name);
  }
}

class FakeNode {
  constructor(ownerDocument, nodeType) {
    this.ownerDocument = ownerDocument;
    this.nodeType = nodeType;
    this.parentNode = null;
    this.childNodes = [];
  }

  append(...nodes) {
    for (const input of nodes) {
      const node = typeof input === 'string'
        ? this.ownerDocument.createTextNode(input)
        : input;
      if (node.nodeType === 11) {
        this.append(...node.childNodes.slice());
        node.childNodes = [];
        continue;
      }
      if (node.parentNode) {
        node.parentNode.childNodes = node.parentNode.childNodes.filter(child => child !== node);
      }
      node.parentNode = this;
      this.childNodes.push(node);
    }
  }

  replaceChildren(...nodes) {
    for (const child of this.childNodes) child.parentNode = null;
    this.childNodes = [];
    this.append(...nodes);
  }

  get textContent() {
    return this.childNodes.map(child => child.textContent).join('');
  }

  set textContent(value) {
    const text = String(value ?? '');
    this.replaceChildren(...(text ? [this.ownerDocument.createTextNode(text)] : []));
  }

  get children() {
    return this.childNodes.filter(child => child.nodeType === 1);
  }
}

export class FakeTextNode extends FakeNode {
  constructor(ownerDocument, value) {
    super(ownerDocument, 3);
    this.data = String(value);
  }

  get textContent() {
    return this.data;
  }

  set textContent(value) {
    this.data = String(value ?? '');
  }
}

export class FakeElement extends FakeNode {
  constructor(ownerDocument, tagName) {
    super(ownerDocument, 1);
    this.tagName = String(tagName).toUpperCase();
    this.className = '';
    this.classList = new FakeClassList(this);
    this.attributes = new Map();
    this.dataset = {};
    this.style = {};
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.indeterminate = false;
    this.value = '';
    this.listeners = new Map();
    this.rect = { left: 0, top: 0, width: 0, height: 0 };
  }

  setAttribute(name, value) {
    const text = String(value);
    if (name === 'class') this.className = text;
    else this.attributes.set(name, text);
    if (name === 'id') this.ownerDocument.elementsByID.set(text, this);
    if (name.startsWith('data-')) {
      const key = name.slice(5).replace(/-([a-z])/g, (_, character) => character.toUpperCase());
      this.dataset[key] = text;
    }
  }

  getAttribute(name) {
    if (name === 'class') return this.className || null;
    return this.attributes.get(name) ?? null;
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  dispatchEvent(event) {
    const eventObject = typeof event === 'string' ? { type: event } : event;
    eventObject.target ||= this;
    eventObject.currentTarget = this;
    for (const listener of this.listeners.get(eventObject.type) || []) listener(eventObject);
    return true;
  }

  blur() {
    this.blurred = true;
    if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null;
    this.dispatchEvent({ type: 'blur' });
  }

  focus() {
    if (this.ownerDocument.activeElement && this.ownerDocument.activeElement !== this) {
      this.ownerDocument.activeElement.blur();
    }
    this.ownerDocument.activeElement = this;
    this.dispatchEvent({ type: 'focus' });
  }

  scrollIntoView(options) {
    this.lastScroll = options;
  }

  getBoundingClientRect() {
    return { ...this.rect };
  }

  querySelectorAll(selector) {
    return findAll(this, element => matchesSelector(element, selector));
  }

  querySelector(selector) {
    return this.querySelectorAll(selector)[0] || null;
  }
}

export class FakeDocumentFragment extends FakeNode {
  constructor(ownerDocument) {
    super(ownerDocument, 11);
  }
}

export class FakeDocument {
  constructor() {
    this.elementsByID = new Map();
    this.documentElement = { clientWidth: 1024 };
    this.title = '';
    this.activeElement = null;
  }

  createElement(tagName) {
    return new FakeElement(this, tagName);
  }

  createTextNode(value) {
    return new FakeTextNode(this, value);
  }

  createDocumentFragment() {
    return new FakeDocumentFragment(this);
  }

  getElementById(id) {
    return this.elementsByID.get(id) || null;
  }

  registerElement(id, tagName = 'div', className = '') {
    const element = this.createElement(tagName);
    element.setAttribute('id', id);
    element.className = className;
    return element;
  }
}

function matchesSelector(element, selector) {
  if (!(element instanceof FakeElement)) return false;
  const attributeMatches = [...selector.matchAll(/\[([^=\]]+)(?:="([^"]*)")?\]/g)];
  for (const [, name, expected] of attributeMatches) {
    const actual = element.getAttribute(name);
    if (actual === null || (expected !== undefined && actual !== expected)) return false;
  }
  const withoutAttributes = selector.replace(/\[[^\]]+\]/g, '');
  const id = withoutAttributes.match(/#([\w-]+)/)?.[1];
  if (id && element.getAttribute('id') !== id) return false;
  const classes = [...withoutAttributes.matchAll(/\.([\w-]+)/g)].map(match => match[1]);
  if (classes.some(name => !element.classList.contains(name))) return false;
  const tagName = withoutAttributes.match(/^[a-z][\w-]*/i)?.[0];
  return !tagName || element.tagName === tagName.toUpperCase();
}

export function findAll(root, predicate) {
  const matches = [];
  const visit = node => {
    if (node.nodeType === 1 && predicate(node)) matches.push(node);
    for (const child of node.childNodes || []) visit(child);
  };
  visit(root);
  return matches;
}

export function findFirst(root, predicate) {
  return findAll(root, predicate)[0] || null;
}

export function createStatusDocument() {
  const document = new FakeDocument();
  for (const id of [
    'banner-out', 'svc-out', 'cmd-models', 'term-subtitle', 'probe-comment', 'updated', 'login-time',
  ]) {
    document.registerElement(id);
  }
  document.registerElement('tip', 'div', 'tip');
  return document;
}

export function createElementDocument(ids) {
  const document = new FakeDocument();
  for (const id of ids) document.registerElement(id);
  return document;
}
