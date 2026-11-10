import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { LogOut, Trash2, UserPlus, Users } from "lucide-react";
import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import {
  auth,
  farmsApi,
  laneLabel,
  roleLabels,
  type Assisted,
  type Identity,
  type Lane,
  type Me,
  type MemberView,
  type Role,
} from "./account-api";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function Card({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3 rounded-lg border bg-ak-surface p-4" aria-label={title}>
      <h2 className="font-semibold">{title}</h2>
      {children}
    </section>
  );
}

/** A code to read out or write down, big. */
function Code({ code, note }: { code: string; note: string }) {
  return (
    <div className="rounded-md border border-dashed p-3 text-center">
      <p className="font-mono text-2xl tracking-widest" aria-label="Code">
        {code}
      </p>
      <p className="text-xs text-muted-foreground">{note}</p>
    </div>
  );
}

function who(id: Identity): string {
  return id.email ?? id.phone ?? (id.lane === "password" ? id.subject : id.name) ?? id.subject;
}

/**
 * The account page: ways to sign in (link Google, a password, a phone),
 * the farms the account is on and the people on this one, a partner's
 * assisted sign-up, and signing out.
 */
export function AccountPage({
  me,
  lanes,
  onChanged,
  onSwitch,
}: {
  me: Me;
  lanes: Lane[];
  onChanged: () => void;
  onSwitch: (holding: string) => void;
}) {
  const [error, setError] = useState("");
  const [ids, setIds] = useState<Identity[]>();
  const [people, setPeople] = useState<MemberView[]>();
  const owner = me.role === "owner";
  const fail = (e: unknown) => setError(message(e));

  const reload = useCallback(() => {
    auth.identities().then(setIds, fail);
    farmsApi.members(me.holding.id).then(setPeople, fail);
  }, [me.holding.id]);
  useEffect(reload, [reload]);

  const signOut = (all: boolean) => {
    (all ? auth.signOutAll() : auth.signOut()).then(() => window.location.assign("/"), fail);
  };

  return (
    <div className="flex max-w-2xl flex-col gap-4">
      <h1 className="text-2xl font-semibold">Account</h1>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      <Card title="You">
        <NameForm name={me.account.name} onSave={(n) => auth.rename(n).then(onChanged, fail)} />
        <h3 className="text-sm font-medium">Ways you sign in</h3>
        <ul className="flex flex-col gap-1 text-sm">
          {ids?.map((i) => (
            <li key={i.lane + i.subject} className="flex items-center justify-between gap-2">
              <span>
                {laneLabel(i.lane)}: <span className="text-muted-foreground">{who(i)}</span>
              </span>
              {ids.length > 1 && (
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={`Remove ${laneLabel(i.lane)}`}
                  onClick={() => auth.unlink(i.lane, i.subject).then(reload, fail)}
                >
                  <Trash2 />
                </Button>
              )}
            </li>
          ))}
          {ids?.length === 0 && (
            <li className="text-ak-warn">
              You have no way to sign in again yet except a code from your field partner. Add one below.
            </li>
          )}
        </ul>
        <AddWays lanes={lanes} ids={ids ?? []} onAdded={reload} onError={fail} />
      </Card>

      <Card title="Your farms">
        <ul className="flex flex-col gap-1 text-sm">
          {me.farms.map((f) => (
            <li key={f.holding.id} className="flex items-center justify-between gap-2">
              <span>
                {f.holding.name} <span className="text-muted-foreground">({roleLabels[f.role]})</span>
              </span>
              {f.holding.id === me.holding.id ? (
                <span className="text-xs text-muted-foreground">Open now</span>
              ) : (
                <Button variant="outline" size="sm" onClick={() => onSwitch(f.holding.id)}>
                  Open
                </Button>
              )}
            </li>
          ))}
        </ul>
        <JoinForm
          onJoined={(h) => {
            onSwitch(h);
          }}
          onError={fail}
        />
      </Card>

      <Card title={`People on ${me.holding.name}`}>
        {owner && (
          <NameForm
            label="Farm name"
            name={me.holding.name}
            onSave={(n) => farmsApi.rename(me.holding.id, n).then(onChanged, fail)}
          />
        )}
        <ul className="flex flex-col gap-2 text-sm">
          {people?.map((p) => (
            <Person key={p.account} p={p} me={me} onChanged={reload} onError={fail} />
          ))}
        </ul>
        {owner && <InviteForm holding={me.holding.id} onError={fail} />}
      </Card>

      {me.partner && <AssistForm onError={fail} />}

      <div className="flex flex-wrap gap-2">
        <Button variant="outline" onClick={() => signOut(false)}>
          <LogOut /> Sign out
        </Button>
        <Button variant="ghost" onClick={() => signOut(true)}>
          Sign out on every phone
        </Button>
      </div>
    </div>
  );
}

function NameForm({
  name,
  label = "Your name",
  onSave,
}: {
  name: string;
  label?: string;
  onSave: (n: string) => void;
}) {
  const [v, setV] = useState(name);
  return (
    <form
      className="flex items-end gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(v);
      }}
    >
      <label className="flex flex-1 flex-col gap-1 text-sm">
        {label}
        <Input value={v} onChange={(e) => setV(e.target.value)} />
      </label>
      <Button type="submit" variant="outline" disabled={v.trim() === "" || v === name}>
        Save
      </Button>
    </form>
  );
}

function AddWays({
  lanes,
  ids,
  onAdded,
  onError,
}: {
  lanes: Lane[];
  ids: Identity[];
  onAdded: () => void;
  onError: (e: unknown) => void;
}) {
  const [user, setUser] = useState(ids.find((i) => i.lane === "password")?.subject ?? "");
  const [pass, setPass] = useState("");
  const [to, setTo] = useState("");
  const [sent, setSent] = useState("");
  const [code, setCode] = useState("");
  const has = (lane: string) => ids.some((i) => i.lane === lane);
  const redirect = lanes.filter((l) => l.kind === "redirect" && !has(l.name));
  const codeLane = lanes.find((l) => l.kind === "code");
  const submit = (e: FormEvent, p: () => Promise<unknown>, done: () => void) => {
    e.preventDefault();
    p().then(() => {
      done();
      onAdded();
    }, onError);
  };
  return (
    <div className="flex flex-col gap-3">
      {redirect.map((l) => (
        <Button key={l.name} variant="outline" onClick={() => auth.start(l.name, true).catch(onError)}>
          Add {laneLabel(l.name)}
        </Button>
      ))}
      {lanes.some((l) => l.name === "password") && (
        <form
          className="flex flex-col gap-2"
          aria-label="Password"
          onSubmit={(e) =>
            submit(
              e,
              () => auth.setPassword(user, pass),
              () => setPass(""),
            )
          }
        >
          <h3 className="text-sm font-medium">
            {has("password") ? "Change your password" : "Add a username and password"}
          </h3>
          <Input aria-label="Username" autoComplete="username" value={user} onChange={(e) => setUser(e.target.value)} />
          <Input
            aria-label="New password (8 or more letters)"
            type="password"
            autoComplete="new-password"
            value={pass}
            onChange={(e) => setPass(e.target.value)}
          />
          <Button type="submit" variant="outline" disabled={user === "" || pass.length < 8}>
            Save password
          </Button>
        </form>
      )}
      {codeLane && (
        <form
          className="flex flex-col gap-2"
          aria-label={`Add ${laneLabel(codeLane.name)}`}
          onSubmit={(e) => {
            e.preventDefault();
            if (!sent) {
              auth.send(codeLane.name, to).then((r) => setSent(r.to), onError);
            } else {
              submit(
                e,
                () => auth.verify(codeLane.name, sent, code, true),
                () => {
                  setSent("");
                  setCode("");
                  setTo("");
                },
              );
            }
          }}
        >
          <h3 className="text-sm font-medium">Add your {laneLabel(codeLane.name).toLowerCase()}</h3>
          <Input
            aria-label={laneLabel(codeLane.name)}
            value={to}
            disabled={sent !== ""}
            onChange={(e) => setTo(e.target.value)}
          />
          {sent && (
            <Input
              aria-label="The code you got"
              inputMode="numeric"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          )}
          <Button type="submit" variant="outline" disabled={to === ""}>
            {sent ? "Add" : "Send me a code"}
          </Button>
        </form>
      )}
    </div>
  );
}

function JoinForm({ onJoined, onError }: { onJoined: (holding: string) => void; onError: (e: unknown) => void }) {
  const [code, setCode] = useState("");
  return (
    <form
      className="flex items-end gap-2"
      aria-label="Join a farm"
      onSubmit={(e) => {
        e.preventDefault();
        farmsApi.join(code).then((m) => onJoined(m.holding.id), onError);
      }}
    >
      <label className="flex flex-1 flex-col gap-1 text-sm">
        Join a farm with a code
        <Input placeholder="XXXX-XXXX-XXXX" value={code} onChange={(e) => setCode(e.target.value)} />
      </label>
      <Button type="submit" variant="outline" disabled={code.trim() === ""}>
        Join
      </Button>
    </form>
  );
}

const roles: Role[] = ["owner", "family", "worker"];

function Person({
  p,
  me,
  onChanged,
  onError,
}: {
  p: MemberView;
  me: Me;
  onChanged: () => void;
  onError: (e: unknown) => void;
}) {
  const [code, setCode] = useState("");
  const self = p.account === me.account.id;
  return (
    <li className="flex flex-col gap-2 rounded-md border p-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span>
          {p.name || "Someone"} {self && <span className="text-muted-foreground">(you)</span>}
        </span>
        {me.role === "owner" && !self && p.role !== "partner" ? (
          <select
            aria-label={`Role of ${p.name}`}
            className="rounded-md border bg-background px-2 py-1 text-sm"
            value={p.role}
            onChange={(e) =>
              farmsApi.setRole(me.holding.id, p.account, e.target.value as Role).then(onChanged, onError)
            }
          >
            {roles.map((r) => (
              <option key={r} value={r}>
                {roleLabels[r]}
              </option>
            ))}
          </select>
        ) : (
          <span className="text-muted-foreground">{roleLabels[p.role]}</span>
        )}
      </div>
      <div className="flex flex-wrap gap-2">
        {me.role === "partner" && !self && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => farmsApi.signInCode(me.holding.id, p.account).then((r) => setCode(r.code), onError)}
          >
            New sign-in code
          </Button>
        )}
        {(me.role === "owner" || self) && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => farmsApi.remove(me.holding.id, p.account).then(onChanged, onError)}
          >
            {self ? "Leave this farm" : "Remove"}
          </Button>
        )}
      </div>
      {code && <Code code={code} note="Works once, for 7 days. Their other phones are signed out." />}
    </li>
  );
}

function InviteForm({ holding, onError }: { holding: string; onError: (e: unknown) => void }) {
  const [role, setRole] = useState<Role>("family");
  const [code, setCode] = useState("");
  return (
    <form
      className="flex flex-col gap-2"
      aria-label="Add a person"
      onSubmit={(e) => {
        e.preventDefault();
        farmsApi.invite(holding, role).then((r) => setCode(r.code), onError);
      }}
    >
      <div className="flex items-end gap-2">
        <label className="flex flex-1 flex-col gap-1 text-sm">
          Add a person as
          <select
            className="rounded-md border bg-background px-2 py-2 text-sm"
            value={role}
            onChange={(e) => setRole(e.target.value as Role)}
          >
            {roles.map((r) => (
              <option key={r} value={r}>
                {roleLabels[r]}
              </option>
            ))}
          </select>
        </label>
        <Button type="submit" variant="outline">
          <UserPlus /> Make a code
        </Button>
      </div>
      {code && (
        <Code code={code} note="They sign in, open Account and type it under Join a farm. Works once, for 7 days." />
      )}
    </form>
  );
}

function AssistForm({ onError }: { onError: (e: unknown) => void }) {
  const [name, setName] = useState("");
  const [phone, setPhone] = useState("");
  const [farm, setFarm] = useState("");
  const [done, setDone] = useState<Assisted>();
  return (
    <Card title="Sign a farmer up">
      <p className="text-sm text-muted-foreground">
        For a farmer without a smartphone account: make their account and farm here, then give them the code. They type
        it on their phone to sign in, and can add Google or a password later.
      </p>
      <form
        className="flex flex-col gap-2"
        aria-label="Sign a farmer up"
        onSubmit={(e) => {
          e.preventDefault();
          farmsApi.assist(name, phone, farm).then((r) => {
            setDone(r);
            setName("");
            setPhone("");
            setFarm("");
          }, onError);
        }}
      >
        <Input
          aria-label="Farmer's name"
          placeholder="Farmer's name"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          aria-label="Phone number (optional)"
          placeholder="Phone number (optional)"
          type="tel"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
        />
        <Input
          aria-label="Farm name (optional)"
          placeholder="Farm name (optional)"
          value={farm}
          onChange={(e) => setFarm(e.target.value)}
        />
        <Button type="submit" disabled={name.trim() === ""}>
          <Users /> Make the account
        </Button>
      </form>
      {done && (
        <Code
          code={done.code}
          note={`${done.account.name}'s sign-in code. Works once, for 7 days. You can open ${done.holding.name} from Your farms.`}
        />
      )}
    </Card>
  );
}
