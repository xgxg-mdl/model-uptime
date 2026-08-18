import assert from 'node:assert/strict';
import test from 'node:test';

import {
  createTerminalIntro,
  terminalMotionDisabled,
} from '../../internal/httpserver/web/assets/scripts/terminal-intro.js';
import { createElementDocument } from './helpers/fake-dom.js';

function introElements() {
  const document = createElementDocument([
    'terminal', 'command-one', 'command-two', 'output-one', 'args-two', 'output-two',
  ]);
  document.getElementById('terminal').className = 'term terminal-intro';
  return document;
}

test('命令依次输入并等待首批数据后显示输出', () => {
  const document = introElements();
  const scheduled = [];
  const intro = createTerminalIntro({
    root: document.getElementById('terminal'),
    schedule(callback, delay) {
      scheduled.push({ callback, delay });
      return scheduled.length;
    },
    stages: [
      {
        command: document.getElementById('command-one'),
        duration: 100,
        pause: 20,
        reveal: [document.getElementById('output-one')],
      },
      {
        command: document.getElementById('command-two'),
        duration: 200,
        reveal: [document.getElementById('args-two'), document.getElementById('output-two')],
      },
    ],
    initialDelay: 10,
    revealDuration: 30,
  });

  intro.start();
  intro.start();
  assert.deepEqual(scheduled.map(task => task.delay), [10]);

  scheduled.shift().callback();
  assert.ok(document.getElementById('command-one').classList.contains('terminal-command-active'));
  assert.equal(scheduled[0].delay, 100);

  scheduled.shift().callback();
  assert.ok(document.getElementById('command-one').classList.contains('terminal-command-waiting'));
  assert.equal(scheduled.length, 0, '数据返回前不应启动下一条命令');

  intro.setDataReady();
  assert.ok(document.getElementById('output-one').classList.contains('terminal-reveal-visible'));
  assert.equal(scheduled[0].delay, 50);

  scheduled.shift().callback();
  assert.ok(document.getElementById('command-two').classList.contains('terminal-command-active'));
  assert.equal(scheduled[0].delay, 200);

  scheduled.shift().callback();
  assert.ok(document.getElementById('args-two').classList.contains('terminal-reveal-visible'));
  assert.ok(document.getElementById('output-two').classList.contains('terminal-reveal-visible'));
  assert.equal(scheduled[0].delay, 30);

  scheduled.shift().callback();
  assert.equal(intro.complete, true);
  assert.ok(document.getElementById('terminal').classList.contains('terminal-intro-complete'));
  assert.equal(document.getElementById('terminal').classList.contains('terminal-intro'), false);

  intro.setDataReady();
  assert.equal(scheduled.length, 0, '轮询更新不应重放首次动画');
});

test('数据先返回时在命令输入完成后立即继续', () => {
  const document = introElements();
  const scheduled = [];
  const intro = createTerminalIntro({
    root: document.getElementById('terminal'),
    schedule(callback, delay) {
      scheduled.push({ callback, delay });
      return scheduled.length;
    },
    stages: [{
      command: document.getElementById('command-one'),
      duration: 100,
      reveal: [document.getElementById('output-one')],
    }],
    initialDelay: 0,
    revealDuration: 30,
  });

  intro.setDataReady();
  intro.start();
  scheduled.shift().callback();
  scheduled.shift().callback();

  assert.ok(document.getElementById('output-one').classList.contains('terminal-reveal-visible'));
  assert.equal(scheduled[0].delay, 30);
});

test('reduced-motion 和后台页面直接显示最终状态', () => {
  assert.equal(terminalMotionDisabled({
    document: { visibilityState: 'visible' },
    window: { matchMedia: () => ({ matches: true }) },
  }), true);
  assert.equal(terminalMotionDisabled({
    document: { visibilityState: 'hidden' },
    window: { matchMedia: () => ({ matches: false }) },
  }), true);

  const document = introElements();
  const intro = createTerminalIntro({
    root: document.getElementById('terminal'),
    stages: [{ command: document.getElementById('command-one'), duration: 100 }],
    schedule() { throw new Error('关闭动效后不应创建定时任务'); },
    disabled: true,
  });
  intro.start();

  assert.equal(intro.complete, true);
  assert.equal(document.getElementById('terminal').classList.contains('terminal-intro'), false);
});
