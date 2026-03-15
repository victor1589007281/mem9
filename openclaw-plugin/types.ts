export interface PluginConfig {
  // Server mode (apiUrl present → server)
  apiUrl?: string;
  tenantID?: string;
  apiToken?: string;
  userToken?: string;

  tenantName?: string;

  // Agent identity for server mode.
  // Defaults to "agent" if not set. Overridden by ctx.agentId at runtime.
  agentName?: string;

  // Ingest: size-aware message selection for smart pipeline
  maxIngestBytes?: number;

  // Memory Agent
  memoryAgentId?: string;
  agentMinSessions?: number;
  agentMaxSessions?: number;
  agentMaxRunsPerSession?: number;
  agentRunTimeoutMs?: number;
  agentIdleReapMs?: number;
  modelFallbackChain?: string[];
}

export interface BulkStoreInput {
  content: string;
  tags?: string[];
  metadata?: Record<string, unknown>;
  memory_type?: "pinned" | "insight";
}

export interface Memory {
  id: string;
  content: string;
  source?: string | null;
  tags?: string[] | null;
  metadata?: Record<string, unknown> | null;
  version?: number;
  updated_by?: string | null;
  created_at: string;
  updated_at: string;
  score?: number;

  // Smart memory pipeline (server mode)
  memory_type?: string;
  state?: string;
  agent_id?: string;
  session_id?: string;
}

export interface SearchResult {
  data: Memory[];
  total: number;
  limit: number;
  offset: number;
}

export interface CreateMemoryInput {
  content: string;
  source?: string;
  tags?: string[];
  metadata?: Record<string, unknown>;
}

export interface UpdateMemoryInput {
  content?: string;
  source?: string;
  tags?: string[];
  metadata?: Record<string, unknown>;
  /** When set, sent as If-Match header to enable server-side conflict detection.
   *  On 409, the plugin merges current + incoming via LLM and retries. */
  _version?: number;
}

export interface SearchInput {
  q?: string;
  tags?: string;
  source?: string;
  limit?: number;
  offset?: number;
}

export interface IngestMessage {
  role: string;
  content: string;
}

export interface IngestInput {
  messages: IngestMessage[];
  session_id: string;
  agent_id: string;
  mode?: "smart" | "raw";
}

export interface IngestResult {
  status: "accepted" | "complete" | "partial" | "failed";
  memories_changed?: number;
  insight_ids?: string[];
  warnings?: number;
  error?: string;
}

export type StoreResult = Memory | IngestResult;
