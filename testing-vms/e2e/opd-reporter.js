function duration(ms) {
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return rest === 0 ? `${minutes}m` : `${minutes}m${rest}s`;
}

function line(indent, text) {
  console.log(`[opd-pw] ${'  '.repeat(indent)}${text}`);
}

function key(value) {
  return value.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'unnamed';
}

function stepAncestors(step) {
  const ancestors = [];
  for (let cursor = step.parent; cursor; cursor = cursor.parent) {
    if (cursor.category === 'test.step') ancestors.unshift(cursor);
  }
  return ancestors;
}

function stepPath(step) {
  return ['flows', 'test', ...stepAncestors(step).map(s => key(s.title)), key(step.title)].join('.');
}

function stepIndent(step) {
  return 2 + stepAncestors(step).length;
}

export default class OpenDeployReporter {
  constructor() {
    this.tests = new Map();
    this.steps = new Map();
  }

  onTestBegin(test) {
    this.tests.set(test.id, Date.now());
    line(1, `step=flows.test starting - ${test.title}`);
  }

  onStepBegin(test, result, step) {
    if (step.category !== 'test.step') return;
    this.steps.set(step, Date.now());
    line(stepIndent(step), `step=${stepPath(step)} starting - ${step.title}`);
  }

  onStepEnd(test, result, step) {
    if (step.category !== 'test.step') return;
    const start = this.steps.get(step) || Date.now();
    const status = step.error ? 'failed' : 'finished';
    line(stepIndent(step), `step=${stepPath(step)} ${status} - took: ${duration(Date.now() - start)}`);
  }

  onTestEnd(test, result) {
    const start = this.tests.get(test.id) || Date.now();
    const status = result.status === 'passed' ? 'finished' : 'failed';
    line(1, `step=flows.test ${status} - took: ${duration(Date.now() - start)}`);
  }
}
