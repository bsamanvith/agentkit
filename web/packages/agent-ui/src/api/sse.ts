// Server-sent events framing, for streams read with fetch (EventSource can't
// POST or set Authorization). The rules and their tests are a port of
// pkg/agentclient's (TestSSEFraming, referenceSSE):
//
//   - lines end in LF, CRLF or CR, mixed freely; a leading BOM is skipped;
//   - "data:" lines (one optional space after the colon) join with "\n";
//   - a blank line ends an event; an event with no data is not one;
//   - lines starting with ":" are comments; other fields (id, event, retry,
//     anything unknown) are ignored: the event's type is in its JSON;
//   - an event cut off by the end of the stream (no blank line) is dropped.
//
// A line, or an event's data, longer than maxBytes fails the stream with
// ResponseTooLargeError, so a server that never ends a line can't exhaust
// memory.

/** The default limit on one line and on one event's data: 8 MiB of characters. */
export const defaultMaxEventBytes = 8 << 20;

export class ResponseTooLargeError extends Error {
  constructor(limit: number) {
    super(`event stream: a line or an event is over ${limit} characters`);
    this.name = "ResponseTooLargeError";
  }
}

/**
 * The data of each event in an SSE body, in order. It returns at the end of
 * the body, dropping an unterminated event. Use `endedCleanly` from
 * {@link SSEReader} to tell whether the body ended between events.
 */
export class SSEReader {
  #line = "";
  #data: string[] = [];
  #dataLen = 0;
  #hasData = false;
  #pendingCR = false;
  #started = false;
  readonly #max: number;
  /** Complete events not yet taken by next(). */
  readonly #ready: string[] = [];

  constructor(maxBytes: number = defaultMaxEventBytes) {
    this.#max = maxBytes;
  }

  /** Feeds decoded text; complete events become available from take(). */
  push(text: string): void {
    if (!this.#started && text.length > 0) {
      this.#started = true;
      if (text.charCodeAt(0) === 0xfeff) text = text.slice(1);
    }
    let start = 0;
    for (let i = 0; i < text.length; i++) {
      const c = text.charCodeAt(i);
      if (c === 10 /* \n */) {
        if (this.#pendingCR) {
          // The LF of a CRLF split across pushes, or right after a CR line.
          this.#pendingCR = false;
          start = i + 1;
          continue;
        }
        this.#endLine(text.slice(start, i));
        start = i + 1;
      } else if (c === 13 /* \r */) {
        this.#pendingCR = false;
        this.#endLine(text.slice(start, i));
        start = i + 1;
        if (i + 1 < text.length) {
          if (text.charCodeAt(i + 1) === 10) {
            i++;
            start = i + 1;
          }
        } else {
          this.#pendingCR = true;
        }
      } else if (this.#pendingCR) {
        this.#pendingCR = false;
      }
    }
    if (start < text.length) {
      this.#line += text.slice(start);
      if (this.#line.length > this.#max) throw new ResponseTooLargeError(this.#max);
    }
  }

  /** Takes the events completed so far. */
  take(): string[] {
    return this.#ready.splice(0);
  }

  #endLine(rest: string) {
    const line = this.#line + rest;
    this.#line = "";
    if (line.length > this.#max) throw new ResponseTooLargeError(this.#max);
    if (line === "") {
      if (this.#hasData) this.#ready.push(this.#data.join("\n"));
      this.#data = [];
      this.#dataLen = 0;
      this.#hasData = false;
      return;
    }
    if (line.charCodeAt(0) === 58 /* : */) return;
    const colon = line.indexOf(":");
    const name = colon < 0 ? line : line.slice(0, colon);
    if (name !== "data") return;
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    this.#dataLen += value.length + 1;
    if (this.#dataLen > this.#max) throw new ResponseTooLargeError(this.#max);
    this.#data.push(value);
    this.#hasData = true;
  }
}

/**
 * Reads an SSE body and yields each event's data. Returns when the body
 * ends; an unterminated last event is dropped.
 */
export async function* readSSE(
  body: ReadableStream<Uint8Array>,
  maxBytes: number = defaultMaxEventBytes,
): AsyncGenerator<string, void, undefined> {
  const reader = body.getReader();
  const decoder = new TextDecoder("utf-8");
  const sse = new SSEReader(maxBytes);
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        sse.push(decoder.decode());
        yield* sse.take();
        return;
      }
      sse.push(decoder.decode(value, { stream: true }));
      yield* sse.take();
    }
  } finally {
    // Stops the download when the caller stops early (run_done, an error,
    // or abandoning the loop); a no-op once the body has ended.
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}
