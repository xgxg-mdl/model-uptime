import assert from 'node:assert/strict';
import test from 'node:test';

import {
  commandTypingMetrics,
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

test('动态模型属于命令文本并决定完整输入时长', () => {
  const command = introElements().getElementById('command-one');
  const commandText = command.ownerDocument.createElement('span');
  const fullCommand = 'monitor --watch gpt-5.6-sol gpt-5.6-terra gpt-5.6-luna gpt-5.5 gpt-5.4 gpt-5.4-mini';
  commandText.className = 'terminal-command-text';
  commandText.textContent = fullCommand;
  command.append(commandText);
  const scheduled = [];
  const frames = [];
  const intro = createTerminalIntro({
    root: command.ownerDocument.getElementById('terminal'),
    stages: [{ command }],
    initialDelay: 0,
    schedule(callback, delay) {
      scheduled.push({ callback, delay });
      return scheduled.length;
    },
    scheduleFrame(callback) {
      frames.push(callback);
      return frames.length;
    },
  });

  assert.deepEqual(
    commandTypingMetrics(fullCommand),
    { characters: 83, duration: 1660 },
  );
  intro.setDataReady();
  intro.start();
  scheduled.shift().callback();
  scheduled.shift().callback();
  assert.equal(scheduled[0].delay, 20);
  assert.equal(command.style['--terminal-command-chars'], '83');
  assert.equal(command.style['--terminal-command-duration'], '1660ms');
  assert.equal(commandText.textContent, 'm');
  command.scrollWidth = 900;
  frames.shift()();
  assert.equal(command.scrollLeft, 900);
  assert.equal(frames.length, 1);
  for (let index = 1; index < 83; index++) scheduled.shift().callback();
  assert.equal(commandText.textContent, fullCommand);
  frames.shift()();
  assert.equal(frames.length, 0, '命令输入完成后应停止滚动跟随');
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
