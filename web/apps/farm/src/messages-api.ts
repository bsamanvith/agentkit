import { call } from "./api";
import type { NotifyStatus, PushSubscriptionJSON, Recipient, RecipientDraft, TestResult } from "./types";

const enc = encodeURIComponent;

/** The message lanes' API (/api/notify), for the Messages page only: it stays out of the first load. */
export const notifyApi = {
  notify: () => call<NotifyStatus>("GET", "/api/notify"),
  recipients: async () => (await call<{ recipients: Recipient[] }>("GET", "/api/notify/recipients")).recipients,
  addRecipient: (d: RecipientDraft) => call<Recipient>("POST", "/api/notify/recipients", d),
  updateRecipient: (id: string, d: RecipientDraft) => call<Recipient>("PUT", `/api/notify/recipients/${enc(id)}`, d),
  removeRecipient: (id: string) => call<undefined>("DELETE", `/api/notify/recipients/${enc(id)}`),
  consent: (id: string, lane: string, optedIn: boolean) =>
    call<Recipient>("PUT", `/api/notify/recipients/${enc(id)}/consent/${enc(lane)}`, { opted_in: optedIn }),
  addPush: (id: string, s: PushSubscriptionJSON) =>
    call<Recipient>("POST", `/api/notify/recipients/${enc(id)}/push`, s),
  dropPush: (id: string, endpoint: string) =>
    call<undefined>("DELETE", `/api/notify/recipients/${enc(id)}/push`, { endpoint }),
  linkTelegram: (id: string) =>
    call<{ code: string; telegram_url?: string }>("POST", `/api/notify/recipients/${enc(id)}/link`),
  testMessage: (id: string, lane?: string) =>
    call<TestResult>("POST", `/api/notify/recipients/${enc(id)}/test`, lane ? { lane } : {}),
};
