const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)';

export function terminalMotionDisabled({ document: documentRef, window: windowRef } = {}) {
  if (documentRef?.visibilityState === 'hidden') return true;
  return Boolean(windowRef?.matchMedia?.(REDUCED_MOTION_QUERY).matches);
}

/** 命令输入与首批数据并行推进，避免网络速度改变终端执行顺序。 */
export function createTerminalIntro({
  root,
  stages = [],
  schedule = globalThis.setTimeout,
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
    stage.command.classList.add('terminal-command-active');
    schedule(() => {
      stage.command.classList.remove('terminal-command-active');
      stage.command.classList.add('terminal-command-waiting');
      waitingForData = true;
      if (dataReady) revealStage();
    }, stage.duration);
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
