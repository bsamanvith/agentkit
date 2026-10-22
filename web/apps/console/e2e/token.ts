// The API token the smoke test's server wants.
export const token = "smoke-token-0123456789abcdef";

// Where the fake model (fake-model.ts) listens.
export const modelPort = 18479;

// The Tasks tests' own server (e2e/tasks.json: two agents that
// message each other) and its fake model, so their held answers and runs
// don't meet the other tests'.
export const tasksPort = 18480;
export const tasksModelPort = 18481;
// The admin pages' server (admin.spec.ts, e2e/admin.json): local users, a
// credential vault, and the fake OAuth2 provider (fake-provider.ts).
export const adminPort = 18482;
export const providerPort = 18483;
export const users = {
  admin: { username: "ada", password: "admin-pass-t064" },
  user: { username: "uma", password: "user-pass-t064" },
};
// Values that must never reach a page: the server's Exa key, a key typed
// into the Connections dialog, and the provider's tokens.
export const serverKeyCanary = "t064-server-key-canary-5e1d";
export const typedKeyCanary = "t064-typed-key-canary-90ab";
export const providerCanary = "t064-provider-canary-c3f7";
