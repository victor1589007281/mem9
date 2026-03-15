import type { MemoryBackend } from "./backend.js";
import { ServerBackend } from "./server-backend.js";
import { registerHooks } from "./hooks.js";
import { MemoryAgent, MEMORY_AGENT_ID } from "./memory-agent.js";
import type { SubagentRuntime, ReconcileEvent } from "./memory-agent.js";
import type {
  PluginConfig,
  CreateMemoryInput,
  UpdateMemoryInput,
  SearchInput,
  IngestInput,
  IngestResult,
  BulkStoreInput,
  Memory,
} from "./types.js";

const DEFAULT_API_URL = "https://api.vmem.ai";

function jsonResult(data: unknown) {
  return data;
}

// ---------------------------------------------------------------------------
// OpenClaw Plugin API types
// ---------------------------------------------------------------------------

interface OpenClawPluginApi {
  pluginConfig?: unknown;
  logger: {
    info: (...args: unknown[]) => void;
    error: (...args: unknown[]) => void;
  };
  registerTool: (
    factory: ToolFactory | (() => AnyAgentTool[]),
    opts: { names: string[] }
  ) => void;
  on: (hookName: string, handler: (...args: unknown[]) => unknown, opts?: { priority?: number }) => void;
  runtime?: {
    subagent?: SubagentRuntime;
    [key: string]: unknown;
  };
}

interface ToolContext {
  workspaceDir?: string;
  agentId?: string;
  sessionKey?: string;
  messageChannel?: string;
}

type ToolFactory = (ctx: ToolContext) => AnyAgentTool | AnyAgentTool[] | null | undefined;

interface AnyAgentTool {
  name: string;
  label: string;
  description: string;
  parameters: {
    type: "object";
    properties: Record<string, unknown>;
    required: string[];
  };
  execute: (_id: string, params: unknown) => Promise<unknown>;
}

// ---------------------------------------------------------------------------
// Tools — deterministic server operations
// ---------------------------------------------------------------------------

function buildTools(backend: MemoryBackend): AnyAgentTool[] {
  return [
    {
      name: "vmem_store",
      label: "Store Memory",
      description: "Store a memory. Returns the stored memory with its assigned id.",
      parameters: {
        type: "object",
        properties: {
          content: { type: "string", description: "Memory content (required, max 50000 chars)" },
          source: { type: "string", description: "Which agent wrote this memory" },
          tags: { type: "array", items: { type: "string" }, description: "Filterable tags (max 20)" },
          metadata: { type: "object", description: "Arbitrary structured data" },
        },
        required: ["content"],
      },
      async execute(_id: string, params: unknown) {
        try {
          return jsonResult({ ok: true, data: await backend.store(params as CreateMemoryInput) });
        } catch (err) {
          return jsonResult({ ok: false, error: err instanceof Error ? err.message : String(err) });
        }
      },
    },
    {
      name: "vmem_search",
      label: "Search Memories",
      description: "Search memories using hybrid vector + keyword search.",
      parameters: {
        type: "object",
        properties: {
          q: { type: "string", description: "Search query" },
          tags: { type: "string", description: "Comma-separated tags to filter by" },
          source: { type: "string", description: "Filter by source agent" },
          limit: { type: "number", description: "Max results (default 20)" },
          offset: { type: "number", description: "Pagination offset" },
        },
        required: [],
      },
      async execute(_id: string, params: unknown) {
        try {
          return jsonResult({ ok: true, ...(await backend.search((params ?? {}) as SearchInput)) });
        } catch (err) {
          return jsonResult({ ok: false, error: err instanceof Error ? err.message : String(err) });
        }
      },
    },
    {
      name: "vmem_get",
      label: "Get Memory",
      description: "Retrieve a single memory by its id.",
      parameters: {
        type: "object",
        properties: { id: { type: "string", description: "Memory id (UUID)" } },
        required: ["id"],
      },
      async execute(_id: string, params: unknown) {
        try {
          const { id } = params as { id: string };
          const result = await backend.get(id);
          if (!result) return jsonResult({ ok: false, error: "memory not found" });
          return jsonResult({ ok: true, data: result });
        } catch (err) {
          return jsonResult({ ok: false, error: err instanceof Error ? err.message : String(err) });
        }
      },
    },
    {
      name: "vmem_update",
      label: "Update Memory",
      description: "Update an existing memory. Only provided fields are changed.",
      parameters: {
        type: "object",
        properties: {
          id: { type: "string", description: "Memory id to update" },
          content: { type: "string", description: "New content" },
          source: { type: "string", description: "New source" },
          tags: { type: "array", items: { type: "string" }, description: "Replacement tags" },
          metadata: { type: "object", description: "Replacement metadata" },
        },
        required: ["id"],
      },
      async execute(_id: string, params: unknown) {
        try {
          const { id, ...input } = params as { id: string } & UpdateMemoryInput;
          const result = await backend.update(id, input);
          if (!result) return jsonResult({ ok: false, error: "memory not found" });
          return jsonResult({ ok: true, data: result });
        } catch (err) {
          return jsonResult({ ok: false, error: err instanceof Error ? err.message : String(err) });
        }
      },
    },
    {
      name: "vmem_delete",
      label: "Delete Memory",
      description: "Delete a memory by id.",
      parameters: {
        type: "object",
        properties: { id: { type: "string", description: "Memory id to delete" } },
        required: ["id"],
      },
      async execute(_id: string, params: unknown) {
        try {
          const { id } = params as { id: string };
          const deleted = await backend.remove(id);
          if (!deleted) return jsonResult({ ok: false, error: "memory not found" });
          return jsonResult({ ok: true });
        } catch (err) {
          return jsonResult({ ok: false, error: err instanceof Error ? err.message : String(err) });
        }
      },
    },
  ];
}

// ---------------------------------------------------------------------------
// Model priority for Memory Agent
//
// Coding Plan models — optimized for structured tool-calling:
//   1. qwen3-coder-plus  → Fast, 1M context, structured output
//   2. glm-4.7           → Reasoning, Chinese-native
//   3. MiniMax-M2.5      → Fast, good Chinese
//   4. qwen3.5-plus      → Most powerful (fallback)
// ---------------------------------------------------------------------------

const MODEL_FALLBACK_CHAIN = [
  "bailian/qwen3-coder-plus",
  "bailian/glm-4.7",
  "bailian/MiniMax-M2.5",
  "bailian/qwen3.5-plus",
];

// ---------------------------------------------------------------------------
// Plugin definition
// ---------------------------------------------------------------------------

const vmemPlugin = {
  id: "vmem",
  name: "Vmem Memory",
  description: "AI agent memory — hybrid vector + keyword search with dedicated Memory Agent.",

  async register(api: OpenClawPluginApi) {
    const cfg = (api.pluginConfig ?? {}) as PluginConfig;
    const effectiveApiUrl = cfg.apiUrl ?? DEFAULT_API_URL;
    if (!cfg.apiUrl) {
      api.logger.info(`[vmem] apiUrl not configured, using default ${DEFAULT_API_URL}`);
    }

    const agentId = cfg.memoryAgentId ?? MEMORY_AGENT_ID;

    // -------------------------------------------------------------------
    // Tenant resolution
    // -------------------------------------------------------------------
    const configuredTenantID = cfg.tenantID;
    const registerTenant = async (agentName: string): Promise<string> => {
      const backend = new ServerBackend(effectiveApiUrl, "", agentName);
      const result = await backend.register();
      api.logger.info(`[vmem] *** Auto-provisioned tenant_id=${result.id} ***`);
      return result.id;
    };
    let registrationPromise: Promise<string> | null = null;
    const resolveTenantID = (agentName: string): Promise<string> => {
      // If a global tenantID is configured, it acts as a global override.
      // Otherwise, we derive one from the agent name.
      if (configuredTenantID) return Promise.resolve(configuredTenantID);
      return Promise.resolve(`${agentName}-memory-tenant`);
    };

    // -------------------------------------------------------------------
    // Memory Agent — pre-created agent + session pool
    //
    // Session keys use format `agent:<agentId>:subagent:pool-<N>`
    // OpenClaw's deriveAgentFromSessionKey() parses this to route
    // sessions to the pre-created agent's context (workspace, model, etc.)
    // -------------------------------------------------------------------
    const subagentRuntime = api.runtime?.subagent ?? null;
    let memoryAgent: MemoryAgent | null = null;

    if (subagentRuntime) {
      const min = cfg.agentMinSessions ?? 1;
      const max = cfg.agentMaxSessions ?? 6;
      memoryAgent = new MemoryAgent(subagentRuntime, {
        agentId,
        minSessions: min,
        maxSessions: max,
        maxRunsPerSession: cfg.agentMaxRunsPerSession ?? 50,
        runTimeoutMs: cfg.agentRunTimeoutMs ?? 120_000,
        idleReapMs: cfg.agentIdleReapMs ?? 60_000,
      });
      api.logger.info(
        `[vmem] Memory Agent "${agentId}" ready (pool: ${min}–${max} sessions)`
      );
    } else {
      api.logger.error(
        "[vmem] Memory Agent disabled — runtime.subagent not available."
      );
    }

    // -------------------------------------------------------------------
    // before_model_resolve — model fallback chain for Memory Agent
    //
    // If the pre-created agent doesn't have model config,
    // this hook provides the fallback chain.
    // -------------------------------------------------------------------
    const userModelChain = cfg.modelFallbackChain ?? MODEL_FALLBACK_CHAIN;
    api.on("before_model_resolve", (_event: unknown, ctx: unknown) => {
      const context = ctx as { agentId?: string; sessionKey?: string } | undefined;
      const isMemoryAgent =
        context?.agentId === agentId ||
        context?.sessionKey?.startsWith(`agent:${agentId}:`);
      if (!isMemoryAgent) return;
      return { modelOverride: userModelChain[0] };
    }, { priority: 90 });

    // -------------------------------------------------------------------
    // Tool registration
    // -------------------------------------------------------------------
    const factory: ToolFactory = (ctx: ToolContext) => {
      let ctxAgentId = ctx.agentId || cfg.agentName || "agent";

      // If the caller is the memory subagent, try to extract the parent agent ID from the session key
      if (ctxAgentId === agentId && ctx.sessionKey) {
        const parts = ctx.sessionKey.split(":");
        // Format: agent:vmem-memory:subagent:victor:pool-1
        if (parts.length >= 4 && parts[2] === "subagent") {
          ctxAgentId = parts[3];
        }
      }

      return buildTools(
        new LazyServerBackend(
          effectiveApiUrl,
          () => resolveTenantID(ctxAgentId),
          ctxAgentId,
        ),
      );
    };
    api.registerTool(factory, { names: toolNames });

    // -------------------------------------------------------------------
    // Hook registration
    // -------------------------------------------------------------------
    const backendResolver = (agentId: string, sessionKey?: string) => {
      let resolvedId = agentId;

      // Resolve parent agent ID if this is the memory subagent
      if (resolvedId === MEMORY_AGENT_ID && sessionKey) {
        const parts = sessionKey.split(":");
        if (parts.length >= 4 && parts[2] === "subagent") {
          resolvedId = parts[3];
        }
      }

      return new LazyServerBackend(
        effectiveApiUrl,
        () => resolveTenantID(resolvedId),
        resolvedId,
      );
    };

    registerHooks(api, backendResolver, api.logger, memoryAgent, {
      maxIngestBytes: cfg.maxIngestBytes,
    });
  },
};

const toolNames = ["vmem_store", "vmem_search", "vmem_get", "vmem_update", "vmem_delete"];

// ---------------------------------------------------------------------------
// Lazy backend
// ---------------------------------------------------------------------------

class LazyServerBackend implements MemoryBackend {
  private resolved: ServerBackend | null = null;
  private resolving: Promise<ServerBackend> | null = null;

  constructor(
    private apiUrl: string,
    private tenantIDProvider: () => Promise<string>,
    private agentId: string,
  ) {}

  private async resolve(): Promise<ServerBackend> {
    if (this.resolved) return this.resolved;
    if (this.resolving) return this.resolving;
    this.resolving = this.tenantIDProvider().then((tenantID) => {
      const backend = new ServerBackend(this.apiUrl, tenantID, this.agentId);
      this.resolved = backend;
      return backend;
    }).catch((err) => { this.resolving = null; throw err; });
    return this.resolving;
  }

  async store(input: CreateMemoryInput) { return (await this.resolve()).store(input); }
  async search(input: SearchInput) { return (await this.resolve()).search(input); }
  async get(id: string) { return (await this.resolve()).get(id); }
  async update(id: string, input: UpdateMemoryInput) { return (await this.resolve()).update(id, input); }
  async remove(id: string) { return (await this.resolve()).remove(id); }
  async ingest(input: IngestInput): Promise<IngestResult> { return (await this.resolve()).ingest(input); }
  async bulkStore(items: BulkStoreInput[]): Promise<Memory[]> { return (await this.resolve()).bulkStore(items); }
  async gather(facts: string[]): Promise<Memory[]> { return (await this.resolve()).gather(facts); }
  async executeReconcile(
    events: ReconcileEvent[],
    existingIDs: string[],
  ): Promise<{ memories_changed: number; created_ids?: string[]; warnings: number }> {
    return (await this.resolve()).executeReconcile(events, existingIDs);
  }
}

export default vmemPlugin;
