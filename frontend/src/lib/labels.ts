/**
 * Node/server label helpers.
 *
 * Stored `labels_json` may be either a JSON object `{k: v, ...}` or a legacy
 * array of `k=v` strings. The UI works with a flat list of `k=v` strings.
 */

/** 预置可选标签（标签应“可选”而不是自由输入）。 */
export const AVAILABLE_SERVER_TAGS: string[] = [
  'env=prod',
  'env=test',
  'env=staging',
  'env=dev',
  'team=ops',
  'team=dev',
  'team=sec',
  'team=data',
  'region=cn-shanghai',
  'region=cn-beijing',
  'region=ap-southeast',
];

/** Normalize stored labels_json into a flat array of `k=v` strings. */
export function labelsToList(labels?: unknown): string[] {
  if (Array.isArray(labels)) {
    return labels.map((l) => String(l)).filter(Boolean);
  }
  if (labels && typeof labels === 'object') {
    return Object.entries(labels as Record<string, unknown>).map(([k, v]) => `${k}=${String(v)}`);
  }
  return [];
}

/** Convert a flat array of `k=v` strings into a labels object for the API. */
export function listToLabelsObject(list: string[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const item of list) {
    const s = item.trim();
    if (!s) continue;
    const eq = s.indexOf('=');
    if (eq > 0) {
      out[s.slice(0, eq).trim()] = s.slice(eq + 1).trim();
    } else {
      out[s] = 'true';
    }
  }
  return out;
}

/** Merge palette with existing labels so custom labels remain removable. */
export function tagOptions(existing: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const t of [...AVAILABLE_SERVER_TAGS, ...existing]) {
    if (!seen.has(t)) {
      seen.add(t);
      out.push(t);
    }
  }
  return out;
}
