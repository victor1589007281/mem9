/**
 * LLM abstraction layer with tool-calling support.
 *
 * LLMProvider interface:
 * - Current: PluginLLMProvider wraps plugin's llmApiKey/llmBaseUrl/llmModel
 * - Future: OpenClawLLMProvider wraps OpenClaw's global LLM (zero config in plugin)
 *
 * The Memory Agent consumes LLMProvider, never knows which backend it uses.
 */

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

export interface LLMConfig {
  apiKey: string;
  baseUrl: string;
  model: string;
  temperature?: number;
}

export interface ChatMessage {
  role: "system" | "user" | "assistant" | "tool";
  content?: string | null;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
}

export interface ToolCall {
  id: string;
  type: "function";
  function: { name: string; arguments: string };
}

export interface ToolDef {
  type: "function";
  function: {
    name: string;
    description: string;
    parameters: Record<string, unknown>;
  };
}

export interface CompletionRequest {
  messages: ChatMessage[];
  tools?: ToolDef[];
  tool_choice?: "auto" | "required" | { type: "function"; function: { name: string } };
  temperature?: number;
}

export interface CompletionResult {
  content?: string | null;
  tool_calls?: ToolCall[];
}

// ---------------------------------------------------------------------------
// LLMProvider — the abstraction agents depend on
// ---------------------------------------------------------------------------

export interface LLMProvider {
  complete(req: CompletionRequest): Promise<CompletionResult>;
}

// ---------------------------------------------------------------------------
// PluginLLMProvider — current implementation using plugin-level config
// ---------------------------------------------------------------------------

const DEFAULT_TEMPERATURE = 0.1;
const TIMEOUT_MS = 60_000;

interface APIResponse {
  choices?: Array<{
    message?: {
      content?: string | null;
      tool_calls?: Array<{
        id: string;
        type: "function";
        function: { name: string; arguments: string };
      }>;
    };
  }>;
  error?: { message?: string };
}

export class PluginLLMProvider implements LLMProvider {
  private baseUrl: string;
  private apiKey: string;
  private model: string;
  private defaultTemp: number;

  constructor(cfg: LLMConfig) {
    this.baseUrl = cfg.baseUrl.replace(/\/+$/, "");
    this.apiKey = cfg.apiKey;
    this.model = cfg.model;
    this.defaultTemp = cfg.temperature ?? DEFAULT_TEMPERATURE;
  }

  async complete(req: CompletionRequest): Promise<CompletionResult> {
    const temperature = req.temperature ?? this.defaultTemp;
    const body: Record<string, unknown> = {
      model: this.model,
      messages: req.messages,
      temperature,
    };

    if (req.tools && req.tools.length > 0) {
      body.tools = req.tools;
      body.tool_choice = req.tool_choice ?? "auto";
    }

    const result = await this.doRequest(body);

    // If tools were requested but LLM returned content instead of tool_calls,
    // retry with tool_choice: "required" to force tool usage.
    if (req.tools && req.tools.length > 0 && !result.tool_calls?.length && result.content) {
      const retry = await this.doRequest({ ...body, tool_choice: "required" });
      if (retry.tool_calls?.length) return retry;
    }

    return result;
  }

  private async doRequest(body: Record<string, unknown>): Promise<CompletionResult> {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), TIMEOUT_MS);

    try {
      const res = await fetch(`${this.baseUrl}/chat/completions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${this.apiKey}`,
        },
        body: JSON.stringify(body),
        signal: controller.signal,
      });

      const data = (await res.json()) as APIResponse;

      if (!res.ok) {
        // If 400 with tools, retry without tools (provider may not support function calling)
        if (res.status === 400 && body.tools) {
          return this.doRequestFallback(body);
        }
        const msg = data?.error?.message ?? res.statusText;
        throw new Error(`LLM HTTP ${res.status}: ${msg}`);
      }

      const choice = data?.choices?.[0]?.message;
      return {
        content: choice?.content ?? null,
        tool_calls: choice?.tool_calls?.map((tc) => ({
          id: tc.id,
          type: "function" as const,
          function: tc.function,
        })),
      };
    } finally {
      clearTimeout(timeoutId);
    }
  }

  /**
   * Fallback for LLMs that don't support function calling.
   * Sends without tools, asks for JSON, parses from content.
   */
  private async doRequestFallback(originalBody: Record<string, unknown>): Promise<CompletionResult> {
    const body = { ...originalBody };
    delete body.tools;
    delete body.tool_choice;
    body.response_format = { type: "json_object" };

    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), TIMEOUT_MS);

    try {
      const res = await fetch(`${this.baseUrl}/chat/completions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${this.apiKey}`,
        },
        body: JSON.stringify(body),
        signal: controller.signal,
      });

      const data = (await res.json()) as APIResponse;
      if (!res.ok) {
        // Last resort: no tools, no response_format
        delete body.response_format;
        const res2 = await fetch(`${this.baseUrl}/chat/completions`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${this.apiKey}`,
          },
          body: JSON.stringify(body),
          signal: controller.signal,
        });
        const data2 = (await res2.json()) as APIResponse;
        return { content: data2?.choices?.[0]?.message?.content ?? null };
      }

      return { content: data?.choices?.[0]?.message?.content ?? null };
    } finally {
      clearTimeout(timeoutId);
    }
  }
}

// ---------------------------------------------------------------------------
// Legacy helper — kept for backward compatibility
// ---------------------------------------------------------------------------

export async function completeJSON(
  cfg: LLMConfig,
  system: string,
  user: string,
): Promise<string> {
  const provider = new PluginLLMProvider(cfg);
  const result = await provider.complete({
    messages: [
      { role: "system", content: system },
      { role: "user", content: user },
    ],
  });
  if (!result.content) throw new Error("LLM returned no content");
  return result.content;
}
