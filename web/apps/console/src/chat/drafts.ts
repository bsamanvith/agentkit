// Drafts, per conversation plus one for New chat ("new"), in memory and in
// sessionStorage (docs/UI.md "Runs and conversations"). Cleared on send and
// when the token is forgotten.

const prefix = "agentkit.draft.";

export class Drafts {
  #mem = new Map<string, string>();
  #listeners = new Set<() => void>();

  subscribe = (fn: () => void) => {
    this.#listeners.add(fn);
    return () => this.#listeners.delete(fn);
  };

  get(key: string): string {
    let v = this.#mem.get(key);
    if (v === undefined) {
      try {
        v = sessionStorage.getItem(prefix + key) ?? "";
      } catch {
        v = ""; // storage blocked: memory only
      }
      this.#mem.set(key, v);
    }
    return v;
  }

  set(key: string, text: string) {
    if (this.get(key) === text) return;
    this.#mem.set(key, text);
    try {
      if (text === "") sessionStorage.removeItem(prefix + key);
      else sessionStorage.setItem(prefix + key, text);
    } catch {
      // Storage blocked.
    }
    for (const fn of this.#listeners) fn();
  }

  /** A message that didn't go out comes back, before anything typed since. */
  restore(key: string, text: string) {
    const now = this.get(key);
    this.set(key, now.trim() === "" ? text : `${text}\n\n${now}`);
  }

  clear() {
    for (const key of [...this.#mem.keys()]) this.set(key, "");
    try {
      for (let i = sessionStorage.length - 1; i >= 0; i--) {
        const k = sessionStorage.key(i);
        if (k?.startsWith(prefix)) sessionStorage.removeItem(k);
      }
    } catch {
      // Storage blocked.
    }
  }
}
