export function benchmarkHistory(runs) {
  const ordered = [...runs].sort((a, b) => (Date.parse(b.started_at) || 0) - (Date.parse(a.started_at) || 0));
  const latestCaseSet = ordered.find(run => run.case_set)?.case_set || '';
  let best = null;
  for (const run of ordered) {
    if (!latestCaseSet || run.case_set !== latestCaseSet || run.status !== 'completed' || !Number.isFinite(run.scores?.total)) continue;
    if (!best || run.scores.total > best.scores.total) best = run;
  }
  return { runs: ordered, latestCaseSet, bestId: best?.id || '' };
}

export function benchmarkScore(run, key) {
  return run.status === 'completed' && Number.isFinite(run.scores?.[key]) ? run.scores[key] : null;
}

export function benchmarkCSV(runs) {
  const columns = ['id', 'model', 'upstream_model', 'provider', 'model_revision', 'started_at', 'finished_at', 'status', 'case_set', 'total', 'bonus', 'plan_gate', 'tool_call', 'react', 'e2e', 'prompt_tokens', 'completion_tokens', 'total_tokens', 'error'];
  const cell = value => {
    let text = value == null ? '' : String(value);
    // Spreadsheet imports must not interpret model names or error text as formulas.
    if (/^[\s]*[=+\-@]/.test(text)) text = "'" + text;
    return /[",\r\n]/.test(text) ? '"' + text.replaceAll('"', '""') + '"' : text;
  };
  return [columns, ...runs.map(run => [run.id, run.model, run.upstream_model, run.provider, run.model_revision, run.started_at, run.finished_at, run.status, run.case_set, ...['total', 'bonus', 'plan_gate', 'tool_call', 'react', 'e2e'].map(key => benchmarkScore(run, key)), run.usage?.prompt_tokens, run.usage?.completion_tokens, run.usage?.total_tokens, run.error])].map(row => row.map(cell).join(',')).join('\r\n') + '\r\n';
}
