import type { SDKMessage, SDKUserMessage, SDKUserMessageReplay } from "@anthropic-ai/claude-agent-sdk";

type SdkPromptInputMessage = SDKUserMessage | SDKUserMessageReplay;
type QueryFactory = (args: { prompt: AsyncIterable<SdkPromptInputMessage>; options?: any }) => AsyncIterable<SDKMessage>;

type TurnRequest = {
  messages: SdkPromptInputMessage[];
  onMessage: (message: SDKMessage) => void;
};

type TurnResult = {
  result: SDKMessage | null;
};

class AsyncPromptQueue<T> implements AsyncIterable<T> {
  private values: T[] = [];
  private waiters: ((value: IteratorResult<T>) => void)[] = [];
  private closed = false;

  push(value: T) {
    if (this.closed) throw new Error("Claude SDK prompt queue is closed.");
    const waiter = this.waiters.shift();
    if (waiter) waiter({ value, done: false });
    else this.values.push(value);
  }

  close() {
    this.closed = true;
    for (const waiter of this.waiters.splice(0)) {
      waiter({ value: undefined as T, done: true });
    }
  }

  [Symbol.asyncIterator]() {
    return {
      next: () => {
        if (this.values.length > 0) {
          return Promise.resolve({ value: this.values.shift() as T, done: false });
        }
        if (this.closed) {
          return Promise.resolve({ value: undefined as T, done: true });
        }
        return new Promise<IteratorResult<T>>((resolve) => this.waiters.push(resolve));
      },
    };
  }
}

export class WarmClaudeSdkWorker {
  private prompt = new AsyncPromptQueue<SdkPromptInputMessage>();
  private queryStarted = 0;
  private active: {
    resolve: (result: TurnResult) => void;
    reject: (error: Error) => void;
    onMessage: (message: SDKMessage) => void;
  } | null = null;
  private pumpStarted = false;
  private pumpError: Error | null = null;

  constructor(private readonly queryFactory: QueryFactory, private readonly optionsFactory: () => any = () => ({})) {}

  queryStartsForTest() {
    return this.queryStarted;
  }

  hasStarted() {
    return this.queryStarted > 0;
  }

  async turn(request: TurnRequest): Promise<TurnResult> {
    if (this.active) {
      throw new Error("Claude SDK worker already has an active turn.");
    }
    if (this.pumpError) {
      throw this.pumpError;
    }
    this.ensurePump();
    const result = new Promise<TurnResult>((resolve, reject) => {
      this.active = { resolve, reject, onMessage: request.onMessage };
    });
    for (const message of request.messages) this.prompt.push(message);
    return result;
  }

  async stop() {
    this.prompt.close();
  }

  private ensurePump() {
    if (this.pumpStarted) return;
    this.pumpStarted = true;
    this.queryStarted += 1;
    void this.pump();
  }

  private async pump() {
    try {
      for await (const msg of this.queryFactory({ prompt: this.prompt, options: this.optionsFactory() })) {
        this.active?.onMessage(msg);
        if (msg.type === "result") {
          const active = this.active;
          this.active = null;
          active?.resolve({ result: msg });
        }
      }
    } catch (error) {
      const err = error instanceof Error ? error : new Error(String(error));
      this.pumpError = err;
      const active = this.active;
      this.active = null;
      active?.reject(err);
    }
  }
}

export function createWarmClaudeSdkWorkerForTest(args: { query: QueryFactory }) {
  return new WarmClaudeSdkWorker(args.query);
}
