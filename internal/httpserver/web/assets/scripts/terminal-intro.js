const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)';
const CHARACTER_DURATION_MS = 20;

export function commandTypingMetrics(text) {
  const characters = Array.from(String(text)).length;
  return { characters, duration: characters * CHARACTER_DURATION_MS };
}

function commandTextNodes(root) {
  const nodes = [];
  const visit = node => {
    for (const child of node.childNodes || []) {
      if (child.nodeType === 3) nodes.push(child);
      else visit(child);
    }
  };
  visit(root);
  return nodes;
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
    const textNodes = commandTextNodes(commandText || command);
    const segments = textNodes.map(node => Array.from(node.textContent));
    for (const node of textNodes) node.textContent = '';

    let nodeIndex = 0;
    let characterIndex = 0;
    return {
      ...metrics,
      typeNext() {
        while (nodeIndex < segments.length && characterIndex >= segments[nodeIndex].length) {
          nodeIndex += 1;
          characterIndex = 0;
        }
        if (nodeIndex >= segments.length) return true;
        textNodes[nodeIndex].textContent += segments[nodeIndex][characterIndex];
        characterIndex += 1;
        return nodeIndex === segments.length - 1 && characterIndex === segments[nodeIndex].length;
      },
    };
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
    const typing = stage.duration === undefined ? prepareCommand(stage.command) : null;
    stage.command.classList.add('terminal-command-active');
    followTyping(stage.command);
    const finishTyping = () => {
      stage.command.classList.remove('terminal-command-active');
      stage.command.classList.add('terminal-command-waiting');
      stage.command.scrollLeft = stage.command.scrollWidth;
      waitingForData = true;
      if (dataReady) revealStage();
    };
    if (!typing || typing.characters === 0) {
      schedule(finishTyping, stage.duration ?? 0);
      return;
    }
    const typeNext = () => {
      if (typing.typeNext()) {
        finishTyping();
        return;
      }
      schedule(typeNext, CHARACTER_DURATION_MS);
    };
    schedule(typeNext, CHARACTER_DURATION_MS);
  }

  function revealStage() {
    if (!waitingForData || complete) return;
    waitingForData = false;
    const stage = stages[stageIndex];
    stage.command.classList.remove('terminal-command-waiting');
    stage.command.classList.add('terminal-command-complete');
    for (const element of stage.reveal || []) element.classList.add('terminal-reveal-visible');
    stage.onReveal?.();

    const nextDelay = stageIndex + 1 < stages.length ? revealDuration + (stage.pause || 0) : revealDuration;
    schedule(() => {
      stage.onRevealComplete?.();
      startStage(stageIndex + 1);
    }, nextDelay);
  }

  function start() {
    if (started) return;
    started = true;
    const motionDisabled = typeof disabled === 'function' ? disabled() : disabled;
    if (motionDisabled || stages.length === 0) {
      for (const stage of stages) {
        stage.onReveal?.();
        stage.onRevealComplete?.();
      }
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
    get complete() {
      return complete;
    },
  };
}
