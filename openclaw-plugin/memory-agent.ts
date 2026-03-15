/**
 * MemoryAgent — Persistent singleton backed by a pre-created OpenClaw agent.
 *
 * Architecture:
 * - The Memory Agent is pre-created via `openclaw agents add vmem-memory`
 * - Bootstrap files (SOUL.md, IDENTITY.md, etc.) live in the agent's workspace
 * - Model config (primary + fallbacks) is set in `agents.list[].model`
 * - Plugin sends messages via `runtime.subagent.run()` with agent-scoped session keys
 * - Session key format: `agent:<agentId>:subagent:pool-<N>` → auto-routes to agent context
 *
 * Session Pool:
 * - Sessions are REUSED (agent accumulates context from past operations)
 * - Pool auto-scales between minSessions and maxSessions based on demand
 * - Sessions rotate after maxRunsPerSession to prevent context overflow
 * - Idle excess sessions are reaped after idleReapMs
 */

export const MEMORY_AGENT_ID = "vmem-memory";

// ---------------------------------------------------------------------------
// SubagentRuntime — OpenClaw's native sub-agent API
// ---------------------------------------------------------------------------

export interface SubagentRuntime {
  run(params: {
    sessionKey: string;
    message: string;
    extraSystemPrompt?: string;
    lane?: string;
    deliver?: boolean;
    idempotencyKey?: string;
  }): Promise<{ runId: string }>;

  waitForRun(params: {
    runId: string;
    timeoutMs?: number;
  }): Promise<{ status: "ok" | "error" | "timeout"; error?: string }>;

  deleteSession(params: {
    sessionKey: string;
    deleteTranscript?: boolean;
  }): Promise<void>;
}

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

export interface AgentResult {
  success: boolean;
  sessionKey: string;
  durationMs: number;
  poolStats: PoolStats;
  error?: string;
}

export interface PoolStats {
  active: number;
  idle: number;
  pending: number;
  total: number;
}

export interface MemoryAgentOptions {
  /** The agent ID in openclaw.json agents.list[]. Default: "vmem-memory". */
  agentId?: string;
  /** Min sessions kept alive (default: 1). */
  minSessions?: number;
  /** Max sessions the pool can grow to (default: 6). */
  maxSessions?: number;
  /** Max runs per session before rotation (default: 50). */
  maxRunsPerSession?: number;
  /** Timeout per subagent run in ms (default: 120_000). */
  runTimeoutMs?: number;
  /** Idle time (ms) before excess sessions are reaped (default: 60_000). */
  idleReapMs?: number;
}

// ---------------------------------------------------------------------------
// Session — a reusable subagent session
// ---------------------------------------------------------------------------

interface Session {
  key: string;
  runCount: number;
  createdAt: number;
  lastUsedAt: number;
  busy: boolean;
}

// ---------------------------------------------------------------------------
// MemoryAgent
// ---------------------------------------------------------------------------

export class MemoryAgent {
  private readonly subagent: SubagentRuntime;
  private readonly agentId: string;
  private readonly minSessions: number;
  private readonly maxSessions: number;
  private readonly maxRunsPerSession: number;
  private readonly runTimeoutMs: number;
  private readonly idleReapMs: number;

  private readonly sessions: Map<string, Session> = new Map();
  private readonly waitQueue: Array<(session: Session) => void> = [];
  private sessionSeq = 0;
  private reapTimer: ReturnType<typeof setInterval> | null = null;

  constructor(subagent: SubagentRuntime, opts?: MemoryAgentOptions) {
    this.subagent = subagent;
    this.agentId = opts?.agentId ?? MEMORY_AGENT_ID;
    this.minSessions = opts?.minSessions ?? 1;
    this.maxSessions = opts?.maxSessions ?? 6;
    this.maxRunsPerSession = opts?.maxRunsPerSession ?? 50;
    this.runTimeoutMs = opts?.runTimeoutMs ?? 120_000;
    this.idleReapMs = opts?.idleReapMs ?? 60_000;

    this.warmup();
    this.startReaper();
  }

  // -----------------------------------------------------------------------
  // Public API
  // -----------------------------------------------------------------------

  get stats(): PoolStats {
    let active = 0, idle = 0;
    for (const s of this.sessions.values()) {
      if (s.busy) active++; else idle++;
    }
    return { active, idle, pending: this.waitQueue.length, total: this.sessions.size };
  }

  /**
   * Process a conversation.
   *
   * Borrows a session from the pool (or waits if all busy and pool at max).
   * The session is scoped to the pre-created agent — OpenClaw routes by
   * session key format: `agent:<agentId>:subagent:pool-<N>`
   */
  async processConversation(conversation: string): Promise<AgentResult> {
    const startMs = Date.now();
    const session = await this.borrowSession();

    try {
      const result = await this.runOnSession(session, conversation);
      return {
        success: result.success,
        sessionKey: session.key,
        durationMs: Date.now() - startMs,
        poolStats: this.stats,
        error: result.error,
      };
    } finally {
      this.returnSession(session);
    }
  }

  /** Process multiple conversations in parallel (bounded by pool). */
  async processConversationsBatch(conversations: string[]): Promise<AgentResult[]> {
    return Promise.all(conversations.map((c) => this.processConversation(c)));
  }

  /** Graceful shutdown — destroy all sessions. */
  async shutdown(): Promise<void> {
    if (this.reapTimer) {
      clearInterval(this.reapTimer);
      this.reapTimer = null;
    }
    const keys = [...this.sessions.keys()];
    this.sessions.clear();
    await Promise.allSettled(
      keys.map((key) =>
        this.subagent.deleteSession({ sessionKey: key, deleteTranscript: true }).catch(() => {})
      )
    );
  }

  // -----------------------------------------------------------------------
  // Session pool
  // -----------------------------------------------------------------------

  private warmup(): void {
    for (let i = 0; i < this.minSessions; i++) {
      this.createSession();
    }
  }

  private createSession(): Session {
    const poolId = ++this.sessionSeq;
    const key = `agent:${this.agentId}:subagent:pool-${poolId}`;
    const session: Session = {
      key,
      runCount: 0,
      createdAt: Date.now(),
      lastUsedAt: Date.now(),
      busy: false,
    };
    this.sessions.set(key, session);
    return session;
  }

  /**
   * Borrow strategy:
   *   1. Pick idle session (least recently used → spread context evenly)
   *   2. If none idle + pool < max → scale up
   *   3. If pool at max → FIFO wait queue
   */
  private async borrowSession(): Promise<Session> {
    let oldest: Session | null = null;
    for (const s of this.sessions.values()) {
      if (!s.busy) {
        if (!oldest || s.lastUsedAt < oldest.lastUsedAt) oldest = s;
      }
    }
    if (oldest) {
      oldest.busy = true;
      return oldest;
    }

    if (this.sessions.size < this.maxSessions) {
      const s = this.createSession();
      s.busy = true;
      return s;
    }

    return new Promise<Session>((resolve) => {
      this.waitQueue.push(resolve);
    });
  }

  private returnSession(session: Session): void {
    session.busy = false;
    session.lastUsedAt = Date.now();

    if (session.runCount >= this.maxRunsPerSession) {
      this.rotateSession(session);
      return;
    }

    const waiter = this.waitQueue.shift();
    if (waiter) {
      session.busy = true;
      waiter(session);
    }
  }

  private rotateSession(old: Session): void {
    this.sessions.delete(old.key);
    this.subagent
      .deleteSession({ sessionKey: old.key, deleteTranscript: true })
      .catch(() => {});

    const fresh = this.createSession();
    const waiter = this.waitQueue.shift();
    if (waiter) {
      fresh.busy = true;
      waiter(fresh);
    }
  }

  // -----------------------------------------------------------------------
  // Idle reaper — scale down
  // -----------------------------------------------------------------------

  private startReaper(): void {
    this.reapTimer = setInterval(() => this.reapIdle(), this.idleReapMs);
    if (this.reapTimer && typeof this.reapTimer === "object" && "unref" in this.reapTimer) {
      (this.reapTimer as NodeJS.Timeout).unref();
    }
  }

  private reapIdle(): void {
    if (this.sessions.size <= this.minSessions) return;

    const now = Date.now();
    const candidates: Session[] = [];
    for (const s of this.sessions.values()) {
      if (!s.busy && (now - s.lastUsedAt) > this.idleReapMs) {
        candidates.push(s);
      }
    }

    candidates.sort((a, b) => a.lastUsedAt - b.lastUsedAt);
    for (const s of candidates) {
      if (this.sessions.size <= this.minSessions) break;
      this.sessions.delete(s.key);
      this.subagent
        .deleteSession({ sessionKey: s.key, deleteTranscript: true })
        .catch(() => {});
    }
  }

  // -----------------------------------------------------------------------
  // Run
  // -----------------------------------------------------------------------

  private async runOnSession(
    session: Session,
    conversation: string,
  ): Promise<{ success: boolean; error?: string }> {
    try {
      const { runId } = await this.subagent.run({
        sessionKey: session.key,
        message: this.buildMessage(conversation),
        lane: "subagent",
      });

      session.runCount++;

      const result = await this.subagent.waitForRun({
        runId,
        timeoutMs: this.runTimeoutMs,
      });

      if (result.status === "ok") return { success: true };
      return { success: false, error: result.error ?? `status: ${result.status}` };
    } catch (err) {
      return { success: false, error: String(err) };
    }
  }

  private buildMessage(conversation: string): string {
    return [
      "Process the following conversation. Extract memorable facts and manage the knowledge base.",
      "",
      "Workflow: search existing → store new / update changed / delete obsolete → finish.",
      "",
      "---",
      "",
      conversation,
    ].join("\n");
  }
}
