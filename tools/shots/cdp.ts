// A minimal Chrome DevTools Protocol client over WebSocket: request/response by
// id, and event subscriptions. Chromium is spoken to directly so the tool needs
// no browser-automation package in web/package.json.

type Pending = {
  resolve: (value: unknown) => void;
  reject: (err: Error) => void;
  timer: ReturnType<typeof setTimeout>;
};

type Listener = (params: Record<string, unknown>, sessionId?: string) => void;

/** The timeouts CDP.connect uses, in milliseconds. */
export type ConnectOptions = {
  // How long the WebSocket has to open.
  connectTimeoutMs?: number;
  // How long a command or event waits before it fails.
  commandTimeoutMs?: number;
};

/** The browser connection, its requests by id, and its event subscriptions. */
export class CDP {
  readonly #ws: WebSocket;
  readonly #pending = new Map<number, Pending>();
  readonly #listeners = new Map<string, Set<Listener>>();
  readonly #commandTimeoutMs: number;
  #nextId = 0;
  #closed: Error | null = null;

  private constructor(ws: WebSocket, commandTimeoutMs: number) {
    this.#ws = ws;
    this.#commandTimeoutMs = commandTimeoutMs;
    ws.onmessage = (event) => this.#receive(String(event.data));
    ws.onclose = () => this.#fail(new Error("the browser connection closed"));
    ws.onerror = () => this.#fail(new Error("the browser connection failed"));
  }

  static connect(url: string, options: ConnectOptions = {}): Promise<CDP> {
    const connectTimeoutMs = options.connectTimeoutMs ?? 15_000;
    const commandTimeoutMs = options.commandTimeoutMs ?? 30_000;
    const { promise, resolve, reject } = Promise.withResolvers<CDP>();
    const ws = new WebSocket(url);
    const timer = setTimeout(() => {
      ws.close();
      reject(new Error(`the browser did not answer on ${url} within ${connectTimeoutMs}ms`));
    }, connectTimeoutMs);
    ws.onopen = () => {
      clearTimeout(timer);
      resolve(new CDP(ws, commandTimeoutMs));
    };
    ws.onerror = () => {
      clearTimeout(timer);
      reject(new Error(`cannot connect to the browser on ${url}`));
    };
    return promise;
  }

  send<T = Record<string, unknown>>(method: string, params: object = {}, sessionId?: string): Promise<T> {
    if (this.#closed !== null) {
      return Promise.reject(this.#closed);
    }
    const id = ++this.#nextId;
    const message: Record<string, unknown> = { id, method, params };
    if (sessionId !== undefined) {
      message.sessionId = sessionId;
    }
    const { promise, resolve, reject } = Promise.withResolvers<T>();
    const timer = setTimeout(() => {
      this.#pending.delete(id);
      reject(new Error(`${method} did not answer within ${this.#commandTimeoutMs}ms`));
    }, this.#commandTimeoutMs);
    this.#pending.set(id, {
      resolve: (value) => resolve(value as T),
      reject,
      timer,
    });
    this.#ws.send(JSON.stringify(message));
    return promise;
  }

  // waitFor resolves on the next event of the given name, for the given session
  // when one is named.
  waitFor(method: string, sessionId: string | null, timeoutMs: number): Promise<Record<string, unknown>> {
    const { promise, resolve, reject } = Promise.withResolvers<Record<string, unknown>>();
    const timer = setTimeout(() => {
      off();
      reject(new Error(`the page never sent ${method} within ${timeoutMs}ms`));
    }, timeoutMs);
    const off = this.on(method, (params, from) => {
      if (sessionId !== null && from !== sessionId) {
        return;
      }
      clearTimeout(timer);
      off();
      resolve(params);
    });
    return promise;
  }

  on(method: string, listener: Listener): () => void {
    const set = this.#listeners.get(method) ?? new Set<Listener>();
    this.#listeners.set(method, set);
    set.add(listener);
    return () => {
      set.delete(listener);
      if (set.size === 0) {
        this.#listeners.delete(method);
      }
    };
  }

  close(): void {
    this.#ws.close();
    this.#fail(new Error("the browser connection was closed"));
  }

  #receive(text: string): void {
    const message = JSON.parse(text) as {
      id?: number;
      result?: unknown;
      error?: { message?: string };
      method?: string;
      params?: Record<string, unknown>;
      sessionId?: string;
    };
    if (message.id !== undefined) {
      const pending = this.#pending.get(message.id);
      if (pending === undefined) {
        return;
      }
      this.#pending.delete(message.id);
      clearTimeout(pending.timer);
      if (message.error !== undefined) {
        pending.reject(new Error(message.error.message ?? "the browser refused the command"));
      } else {
        pending.resolve(message.result ?? {});
      }
      return;
    }
    if (message.method === undefined) {
      return;
    }
    for (const listener of this.#listeners.get(message.method) ?? []) {
      listener(message.params ?? {}, message.sessionId);
    }
  }

  #fail(err: Error): void {
    this.#closed = err;
    for (const [id, pending] of this.#pending) {
      clearTimeout(pending.timer);
      pending.reject(err);
      this.#pending.delete(id);
    }
  }
}
