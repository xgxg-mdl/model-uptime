const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)';
const CHARACTER_DURATION_MS = 32;

export function commandTypingMetrics(text) {
  const characters = Array.from(String(text)).length;
  return { characters, duration: characters * CHARACTER_DURATION_MS };
}

export function terminalMotionDisabled({ document: documentRef, window: windowRef } = {}) {
  if (documentRef?.visibilityState === 'hidden') return true;
  return Boolean(windowRef?.matchMedia?.(REDUCED_MOTION_QUERY).matches);
}

/** 命令输入与首批数据并行推进，避免网络速度改变终端执行顺序。 */
export function createTerminalIntro({
  root,
  stages = [],
  schedule = globalThis.setTimeout,
  scheduleFrame = typeof globalThis.requestAnimationFrame === 'function'
    ? callback => globalThis.requestAnimationFrame(callback)
    : null,
  initialDelay = 180,
  revealDuration = 160,
  disabled = false,
} = {}) {
  if (!root) throw new Error('terminal root is required');

  let started = false;
  let dataReady = false;
  let stageIndex = -1;
  let waitingForData = false;
  let complete = false;

  function setCommandProperty(command, name, value) {
    if (typeof command.style?.setProperty === 'function') command.style.setProperty(name, value);
    else command.style[name] = value;
  }

  function prepareCommand(command) {
    const commandText = command.querySelector('.terminal-command-text');
    const metrics = commandTypingMetrics(commandText?.textContent || '');
    setCommandProperty(command, '--terminal-command-chars', String(metrics.characters));
    setCommandProperty(command, '--terminal-command-duration', `${metrics.duration}ms`);
    return metrics.duration;
  }

  function followTyping(command) {
    if (!scheduleFrame) return;
    const follow = () => {
      if (!command.classList.contains('terminal-command-active')) return;
      command.scrollLeft = command.scrollWidth;
      scheduleFrame(follow);
    };
    scheduleFrame(follow);
  }

  function finish() {
    if (complete) return;
    complete = true;
    root.classList.remove('terminal-intro');
    root.classList.add('terminal-intro-complete');
  }

  function startStage(index) {
    if (index >= stages.length) {
      finish();
      return;
    }

    stageIndex = index;
    const stage = stages[index];
    const duration = stage.duration ?? prepareCommand(stage.command);
    stage.command.classList.add('terminal-command-active');
    followTyping(stage.command);
    schedule(() => {
      stage.command.classList.remove('terminal-command-active');
      stage.command.classList.add('terminal-command-waiting');
      stage.command.scrollLeft = stage.command.scrollWidth;
      waitingForData = true;
      if (dataReady) revealStage();
    }, duration);
  }

  function revealStage() {
    if (!waitingForData || complete) return;
    waitingForData = false;
    const stage = stages[stageIndex];
    stage.command.classList.remove('terminal-command-waiting');
    stage.command.classList.add('terminal-command-complete');
    for (const element of stage.reveal || []) element.classList.add('terminal-reveal-visible');

    const nextDelay = stageIndex + 1 < stages.length
      ? revealDuration + (stage.pause || 0)
      : revealDuration;
    schedule(() => startStage(stageIndex + 1), nextDelay);
  }

  function start() {
    if (started) return;
    started = true;
    if (disabled || stages.length === 0) {
      finish();
      return;
    }
    schedule(() => startStage(0), initialDelay);
  }

  function setDataReady() {
    dataReady = true;
    revealStage();
  }

  return {
    start,
    setDataReady,
    get complete() { return complete; },
  };
}
