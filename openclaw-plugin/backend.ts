import type {
  Memory,
  SearchResult,
  StoreResult,
  CreateMemoryInput,
  UpdateMemoryInput,
  SearchInput,
  IngestInput,
  IngestResult,
  BulkStoreInput,
} from "./types.js";
import type { ReconcileEvent } from "./memory-agent.js";

/**
 * MemoryBackend — pure data transport interface.
 *
 * Implementations (e.g. ServerBackend) handle HTTP communication only.
 * All LLM intelligence lives in MemoryAgent, not here.
 */
export interface MemoryBackend {
  store(input: CreateMemoryInput): Promise<StoreResult>;
  search(input: SearchInput): Promise<SearchResult>;
  get(id: string): Promise<Memory | null>;
  update(id: string, input: UpdateMemoryInput): Promise<Memory | null>;
  remove(id: string): Promise<boolean>;

  ingest(input: IngestInput): Promise<IngestResult>;
  bulkStore(items: BulkStoreInput[]): Promise<Memory[]>;

  /** Server-side parallel gather: search existing memories for facts. */
  gather(facts: string[]): Promise<Memory[]>;

  /** Server-side parallel execute: apply reconcile events. */
  executeReconcile(
    events: ReconcileEvent[],
    existingIDs: string[],
  ): Promise<{ memories_changed: number; created_ids?: string[]; warnings: number }>;
}
