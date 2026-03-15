/**
 * ServerBackend — Pure HTTP transport layer.
 *
 * Implements MemoryBackend via REST calls to vmem-server.
 * No intelligence — all smarts live in the Memory Agent (subagent).
 */

import type { MemoryBackend } from "./backend.js";
import type {
  Memory,
  StoreResult,
  SearchResult,
  CreateMemoryInput,
  UpdateMemoryInput,
  SearchInput,
  IngestInput,
  IngestResult,
  BulkStoreInput,
} from "./types.js";

type ProvisionVmemsResponse = {
  id: string;
};

export class ServerBackend implements MemoryBackend {
  private baseUrl: string;
  private tenantID: string;
  private agentName: string;

  constructor(apiUrl: string, tenantID: string, agentName: string) {
    this.baseUrl = apiUrl.replace(/\/+$/, "");
    this.tenantID = tenantID;
    this.agentName = agentName;
  }

  async register(): Promise<ProvisionVmemsResponse> {
    const resp = await fetch(this.baseUrl + "/v1alpha1/vmems", {
      method: "POST",
      signal: AbortSignal.timeout(8_000),
    });

    if (!resp.ok) {
      const body = await resp.text();
      throw new Error(`vmem provision failed (${resp.status}): ${body}`);
    }

    const data = (await resp.json()) as ProvisionVmemsResponse;
    if (!data?.id) {
      throw new Error("vmem provision did not return tenant ID");
    }

    this.tenantID = data.id;
    return data;
  }

  private tenantPath(path: string): string {
    if (!this.tenantID) {
      throw new Error("tenant ID is not configured");
    }
    return `/v1alpha1/vmems/${this.tenantID}${path}`;
  }

  async store(input: CreateMemoryInput): Promise<StoreResult> {
    return this.request<StoreResult>("POST", this.tenantPath("/memories"), input);
  }

  async search(input: SearchInput): Promise<SearchResult> {
    const params = new URLSearchParams();
    if (input.q) params.set("q", input.q);
    if (input.tags) params.set("tags", input.tags);
    if (input.source) params.set("source", input.source);
    if (input.limit != null) params.set("limit", String(input.limit));
    if (input.offset != null) params.set("offset", String(input.offset));

    const qs = params.toString();
    const raw = await this.request<{
      memories: Memory[];
      total: number;
      limit: number;
      offset: number;
    }>("GET", `${this.tenantPath("/memories")}${qs ? "?" + qs : ""}`);
    return {
      data: raw.memories ?? [],
      total: raw.total,
      limit: raw.limit,
      offset: raw.offset,
    };
  }

  async get(id: string): Promise<Memory | null> {
    try {
      return await this.request<Memory>("GET", this.tenantPath(`/memories/${id}`));
    } catch {
      return null;
    }
  }

  async update(id: string, input: UpdateMemoryInput): Promise<Memory | null> {
    try {
      const headers: Record<string, string> = {};
      if (input._version) {
        headers["If-Match"] = String(input._version);
      }
      return await this.requestWithHeaders<Memory>(
        "PUT",
        this.tenantPath(`/memories/${id}`),
        input,
        headers,
      );
    } catch {
      return null;
    }
  }

  async remove(id: string): Promise<boolean> {
    try {
      await this.request("DELETE", this.tenantPath(`/memories/${id}`));
      return true;
    } catch {
      return false;
    }
  }

  async ingest(input: IngestInput): Promise<IngestResult> {
    return this.request<IngestResult>("POST", this.tenantPath("/memories"), input);
  }

  async bulkStore(items: BulkStoreInput[]): Promise<Memory[]> {
    const resp = await this.request<{ memories?: Memory[] }>(
      "POST",
      this.tenantPath("/memories/bulk"),
      { memories: items }
    );
    return resp.memories ?? [];
  }

  async gather(facts: string[]): Promise<Memory[]> {
    const resp = await this.request<{ existing: Memory[] }>(
      "POST",
      this.tenantPath("/memories/gather"),
      { facts },
    );
    return resp.existing ?? [];
  }

  async executeReconcile(
    events: Array<{ id: string; text: string; event: string; old_memory?: string; tags?: string[] }>,
    existingIDs: string[],
  ): Promise<{ memories_changed: number; created_ids?: string[]; warnings: number }> {
    return this.request<{ memories_changed: number; created_ids?: string[]; warnings: number }>(
      "POST",
      this.tenantPath("/memories/execute"),
      { events, existing_ids: existingIDs },
    );
  }

  // -------------------------------------------------------------------------
  // HTTP transport
  // -------------------------------------------------------------------------

  private async requestWithHeaders<T>(
    method: string,
    path: string,
    body?: unknown,
    extraHeaders?: Record<string, string>,
  ): Promise<T> {
    const url = this.baseUrl + path;
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      "X-Vmem-Agent-Id": this.agentName,
      ...extraHeaders,
    };
    const resp = await fetch(url, {
      method,
      headers,
      body: body != null ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(8_000),
    });

    if (resp.status === 204) {
      return undefined as T;
    }

    const data = await resp.json();
    if (!resp.ok) {
      throw new Error(
        (data as { error?: string }).error || `HTTP ${resp.status}`
      );
    }
    return data as T;
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown
  ): Promise<T> {
    return this.requestWithHeaders<T>(method, path, body);
  }
}
