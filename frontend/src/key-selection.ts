import type { Group, Key } from "./api";

export function usableKey(key: Key, now = Date.now()): boolean {
  return key.status === "active" && (!key.expires_at || new Date(key.expires_at).getTime() > now);
}

export function compatibleClientKeys(client: string, keys: Key[], groups: Group[], now = Date.now()): Key[] {
  return keys.filter(key => {
    if (!usableKey(key, now)) return false;
    const group = groups.find(candidate => candidate.id === key.group_id);
    if (!group) return false;
    if (client === "claude") return group.platform === "anthropic" || group.allow_messages_dispatch;
    if (client === "opencode") return ["openai", "anthropic", "deepseek", "kimi"].includes(group.platform);
    return false;
  });
}

export function initialKeySelections(clientIds: string[], keys: Key[], groups: Group[]): Record<string, string> {
  const selections: Record<string, string> = {};
  for (const client of clientIds) {
    const candidates = compatibleClientKeys(client, keys, groups);
    if (candidates.length === 1) selections[client] = String(candidates[0].id);
  }
  return selections;
}

export function selectClientKey(selections: Record<string, string>, client: string, key: string): Record<string, string> {
  return {...selections, [client]: key};
}

export function validKeySelections(selections: Record<string, string>, clientIds: string[], keys: Key[], groups: Group[]): Record<string, string> {
  return Object.fromEntries(Object.entries(selections).filter(([client, id]) =>
    clientIds.includes(client) && compatibleClientKeys(client, keys, groups).some(key => String(key.id) === id)
  ));
}
