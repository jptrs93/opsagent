import {test} from '@playwright/test';

export async function runCase(ctx, caseDef) {
  if (caseDef.when && !caseDef.when(ctx)) return;

  for (const requirement of caseDef.requires || []) {
    if (!ctx.completedCases?.has(requirement)) {
      throw new Error(`case ${caseDef.id} requires ${requirement}`);
    }
  }

  return test.step(caseDef.title, () => caseDef.run(ctx));
}

export async function runCases(ctx, cases) {
  ctx.completedCases = ctx.completedCases || new Set();
  const seen = new Set();

  for (const caseDef of cases) {
    if (seen.has(caseDef.id)) throw new Error(`duplicate case id: ${caseDef.id}`);
    seen.add(caseDef.id);

    await runCase(ctx, caseDef);
    if (!caseDef.when || caseDef.when(ctx)) ctx.completedCases.add(caseDef.id);
  }
}
