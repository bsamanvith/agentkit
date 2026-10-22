// The API token the smoke test's server wants.
export const token = "smoke-token-0123456789abcdef";

// Where the fake model (fake-model.ts) listens.
export const modelPort = 18479;

// The Tasks tests' own server (e2e/tasks.json: two agents that
// message each other) and its fake model, so their held answers and runs
// don't meet the other tests'.
export const tasksPort = 18480;
export const tasksModelPort = 18481;
