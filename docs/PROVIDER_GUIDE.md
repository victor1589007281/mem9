# mnemos Embedding & LLM Provider 兼容指南

> 详解 mnemos 支持的各种 Embedding 和 LLM 提供商，包括阿里云百炼、OpenAI、Ollama 等，以及百炼 Coding Plan 的适用范围分析。

---

## 目录

- [1. 架构概览](#1-架构概览)
- [2. 阿里云百炼 Coding Plan 适用性分析](#2-阿里云百炼-coding-plan-适用性分析)
- [3. Embedding 提供商对比](#3-embedding-提供商对比)
- [4. LLM 提供商对比](#4-llm-提供商对比)
- [5. 推荐配置方案](#5-推荐配置方案)
- [6. 详细配置示例](#6-详细配置示例)

---

## 1. 架构概览

mnemos 的 Embedding 和 LLM 均采用 **OpenAI 兼容接口**，任何提供 `/v1/embeddings` 和 `/v1/chat/completions` 端点的服务均可接入。

```mermaid
graph TD
    subgraph mnemo-server["<b>mnemo-server</b>"]
        EMB["<b>Embedder</b><br/>POST /v1/embeddings<br/>用于向量搜索"]
        LLM["<b>LLM Client</b><br/>POST /v1/chat/completions<br/>用于事实抽取 + 记忆协调"]
    end

    subgraph Providers["<b>可选 Provider</b>"]
        direction TB
        P1["<b>OpenAI</b><br/>api.openai.com/v1"]
        P2["<b>阿里云百炼</b><br/>dashscope.aliyuncs.com<br/>/compatible-mode/v1"]
        P3["<b>Ollama（本地免费）</b><br/>localhost:11434/v1"]
        P4["<b>其他 OpenAI 兼容</b><br/>vLLM / LM Studio /<br/>Azure OpenAI / DeepSeek"]
    end

    EMB --> P1
    EMB --> P2
    EMB --> P3
    EMB --> P4
    LLM --> P1
    LLM --> P2
    LLM --> P3
    LLM --> P4

    style mnemo-server fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style Providers fill:#f3e5f5,stroke:#7B1FA2,stroke-width:2px
```

**关键环境变量**：

| 变量 | 用途 | 默认值 |
|------|------|--------|
| `MNEMO_EMBED_API_KEY` | Embedding API Key | （空 = 禁用向量搜索） |
| `MNEMO_EMBED_BASE_URL` | Embedding API 地址 | `https://api.openai.com/v1` |
| `MNEMO_EMBED_MODEL` | Embedding 模型名 | `text-embedding-3-small` |
| `MNEMO_EMBED_DIMS` | 向量维度 | `1536` |
| `MNEMO_LLM_API_KEY` | LLM API Key | （空 = 禁用智能 Ingest） |
| `MNEMO_LLM_BASE_URL` | LLM API 地址 | `https://api.openai.com/v1` |
| `MNEMO_LLM_MODEL` | LLM 模型名 | `gpt-4o-mini` |
| `MNEMO_LLM_TEMPERATURE` | LLM 温度 | `0.1` |

---

## 2. 阿里云百炼 Coding Plan 适用性分析

### 2.1 结论速览

```mermaid
graph TD
    CP["<b>百炼 Coding Plan</b><br/>sk-sp-xxx · coding.dashscope"]

    CP -->|"❌ 不支持"| EMB_CP["<b>Embedding</b><br/>Coding Plan 仅含对话模型<br/>不包含 text-embedding 系列"]
    CP -->|"⚠️ 违规风险"| LLM_CP["<b>mnemos LLM</b><br/>Coding Plan 禁止 API 调用<br/>仅限编程工具交互式使用"]

    PAYGO["<b>百炼按量付费</b><br/>普通 API Key · dashscope"]

    PAYGO -->|"✅ 完全支持"| EMB_PG["<b>Embedding</b><br/>text-embedding-v4<br/>100 万 Token 免费额度"]
    PAYGO -->|"✅ 完全支持"| LLM_PG["<b>mnemos LLM</b><br/>qwen-plus / qwen-max<br/>API 调用无限制"]

    style CP fill:#ffebee,stroke:#C62828,stroke-width:2px
    style PAYGO fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style EMB_CP fill:#ffebee,stroke:#C62828,stroke-width:1px
    style LLM_CP fill:#fff8e1,stroke:#F9A825,stroke-width:1px
    style EMB_PG fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
    style LLM_PG fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
```

### 2.2 Coding Plan 不适用于 mnemos 的原因

**原因一：不包含 Embedding 模型**

Coding Plan 支持的模型列表仅限对话模型：

| 模型 | 类型 | Coding Plan |
|------|------|:--:|
| qwen3.5-plus | 对话 | ✅ |
| qwen3-max | 对话 | ✅ |
| qwen3-coder-plus | 对话 | ✅ |
| kimi-k2.5 | 对话 | ✅ |
| glm-5 | 对话 | ✅ |
| MiniMax-M2.5 | 对话 | ✅ |
| **text-embedding-v4** | **Embedding** | **❌ 不包含** |
| **text-embedding-v3** | **Embedding** | **❌ 不包含** |

**原因二：使用条款禁止 API 调用**

Coding Plan 官方明确规定：

> "**仅限在编程工具（如 Claude Code、OpenClaw 等）中使用**，禁止以 API 调用的形式用于自动化脚本、自定义应用程序后端或任何非交互式批量调用场景。"

mnemo-server 以后端服务的方式**程序化调用** `/v1/chat/completions`（事实抽取、记忆协调），属于"自定义应用程序后端"范畴，**违反 Coding Plan 使用条款**。使用 Coding Plan 的 API Key 可能导致订阅被暂停或 Key 被封禁。

**原因三：专属 Base URL 不同**

| 用途 | Base URL | API Key 前缀 |
|------|----------|:--:|
| Coding Plan（编程工具专用） | `https://coding.dashscope.aliyuncs.com/v1` | `sk-sp-` |
| 百炼按量付费（API 调用） | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `sk-` |

### 2.3 Coding Plan 的检测机制

百炼通过多层机制检测 Coding Plan 调用是否来自合规的编程工具：

```mermaid
flowchart TD
    REQ["<b>API 请求到达</b><br/>coding.dashscope.aliyuncs.com"] --> CHECK1{{"API Key 校验"}}
    CHECK1 -- "sk-sp- 前缀 ✅" --> CHECK2{{"请求来源检测"}}
    CHECK1 -- "sk- 前缀 ❌" --> REJECT1["<b>403 invalid api-key</b><br/>路由到按量付费"]

    CHECK2 -- "已知编程工具 ✅" --> PASS["<b>✅ 通过</b><br/>计入 Coding Plan 额度"]
    CHECK2 -- "未知来源 ❌" --> REJECT2["<b>❌ 拒绝</b><br/>Coding Plan is currently<br/>only available for Coding Agents"]

    subgraph 检测手段["<b>来源检测手段（推测）</b>"]
        direction TB
        D1["<b>HTTP Headers</b><br/>User-Agent / 自定义头部<br/>OpenClaw: x-openclaw-account-id<br/>Claude Code: Anthropic 协议头"]
        D2["<b>请求格式特征</b><br/>工具调用结构 / 消息格式<br/>tool_use / tool_result 字段"]
        D3["<b>调用模式分析</b><br/>交互式 vs 批量调用<br/>频率 · 时间分布"]
    end

    style 检测手段 fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style REJECT2 fill:#ffebee,stroke:#C62828,stroke-width:1px
    style PASS fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
```

**已确认的检测行为**（来自官方 FAQ）：

| 工具 | Coding Plan | 说明 |
|------|:--:|------|
| OpenClaw | ✅ | 官方支持 |
| Claude Code | ✅ | 官方支持 |
| Cursor | ✅ | 官方支持 |
| VSCode Cline | ✅ | 官方支持 |
| Qwen Code | ✅ | 官方支持 |
| OpenCode | ✅ | 官方支持 |
| **curl / Postman** | **❌** | 明确禁止 |
| **Dify** | **❌** | 明确禁止 |
| **自定义后端** | **❌** | 明确禁止 |

> 官方 FAQ 原文错误提示：*"Coding Plan is currently only available for Coding Agents"* —— 这表明服务端存在**主动检测机制**，不仅仅是 ToS 约束。

### 2.4 合规利用 Coding Plan 的架构方案

虽然 mnemo-server 作为后端服务**不能直接使用** Coding Plan，但有一种**合规的架构方案**：让 LLM 调用发生在 OpenClaw 插件内部（插件运行在 OpenClaw 进程内），而不是 mnemo-server 中。

```mermaid
graph TD
    subgraph 当前架构["<b>当前架构（mnemo-server 调 LLM）</b>"]
        direction LR
        OC1["<b>OpenClaw</b>"] -->|"agent_end hook"| PLUGIN1["<b>mnemo 插件</b>"]
        PLUGIN1 -->|"POST /memories<br/>（传对话原文）"| SERVER1["<b>mnemo-server</b>"]
        SERVER1 -->|"❌ 后端 API 调用<br/>违反 Coding Plan ToS"| LLM1["<b>LLM API</b>"]
        SERVER1 --> DB1["<b>数据库</b>"]
    end

    subgraph 新架构["<b>方案：插件侧 LLM（合规）</b>"]
        direction LR
        OC2["<b>OpenClaw</b>"] -->|"agent_end hook"| PLUGIN2["<b>mnemo 插件</b><br/>（内置 LLM 调用）"]
        PLUGIN2 -->|"✅ 在 OpenClaw 进程内<br/>调用 Coding Plan"| LLM2["<b>Coding Plan<br/>LLM API</b>"]
        PLUGIN2 -->|"POST /memories<br/>mode=raw<br/>（传已处理的 facts）"| SERVER2["<b>mnemo-server</b><br/>（仅存储/搜索/embedding）"]
        SERVER2 --> DB2["<b>数据库</b>"]
    end

    style 当前架构 fill:#ffebee,stroke:#C62828,stroke-width:2px
    style 新架构 fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

**核心原理**：OpenClaw 插件运行在 OpenClaw Gateway 进程内部（in-process），从插件发出的 LLM 请求**本质上就是从 OpenClaw 发出的**，这是 Coding Plan 允许的合规用法。

**OpenClaw Plugin SDK 提供的关键能力**：

```typescript
// OpenClaw 插件可以通过 runtime API 获取模型认证信息
const auth = await api.runtime.modelAuth.resolveApiKeyForProvider("bailian");

// 也可以通过 llm-task SDK 发起 LLM 调用
import { /* llm task helpers */ } from "openclaw/plugin-sdk/llm-task";
```

**方案对比**：

| 维度 | 当前（服务端 LLM） | 方案（插件侧 LLM） |
|------|-------------------|-------------------|
| LLM 调用位置 | mnemo-server（Go 进程） | OpenClaw 插件（Gateway 进程） |
| Coding Plan 合规 | ❌ 后端 API 调用 | ✅ 编程工具内调用 |
| Embedding | 服务端处理 | 服务端处理（不变） |
| mnemo-server 角色 | 存储 + 搜索 + LLM 调用 | 仅存储 + 搜索 + Embedding |
| 插件复杂度 | 低（仅转发对话） | 中（需实现抽取 + 协调逻辑） |
| 跨编程工具通用 | ✅ 所有客户端通用 | ⚠️ 仅 OpenClaw 受益 |

**实现要点**：

1. 在 `openclaw-plugin/hooks.ts` 的 `agent_end` 钩子中，不再直接调用 `backend.ingest()`（会触发服务端 LLM）
2. 改为在插件内部调用 LLM 进行事实抽取（extractFacts）
3. 将抽取结果通过 `mode=raw` 发送给 mnemo-server 存储
4. mnemo-server 不需要 `MNEMO_LLM_*` 配置，仅需 `MNEMO_EMBED_*`

```typescript
// openclaw-plugin/hooks.ts — agent_end 中的 LLM 调用（概念示例）
api.on("agent_end", async (event) => {
  const messages = event.messages;
  
  // 1. 在插件内部调用 LLM（通过 OpenClaw 的 Coding Plan）
  const facts = await extractFactsViaOpenClawLLM(api, messages);
  
  // 2. 将已处理的 facts 发送给 mnemo-server（mode=raw，不需要服务端 LLM）
  for (const fact of facts) {
    await backend.store({
      content: fact,
      source: "openclaw-auto",
      tags: ["auto-capture"],
    });
  }
});
```

> **注意**：此方案仅对 OpenClaw 用户有效。Claude Code 和 OpenCode 插件如果也想用 Coding Plan，需要各自在插件层实现类似逻辑。对于非编程工具场景，仍需使用按量付费 API 或本地 Ollama。

### 2.5 不推荐：伪装请求头

理论上可以让 mnemo-server 模拟编程工具的 HTTP 请求头（如 User-Agent）来绕过检测，但**强烈不推荐**：

| 风险 | 说明 |
|------|------|
| **违反 ToS** | 明确违反"禁止 API 调用形式用于后端服务"的条款 |
| **Key 封禁** | 被检测到后 `sk-sp-` Key 会被禁用，影响正常编程工具使用 |
| **检测升级** | 阿里云可以随时升级检测机制（如请求指纹、调用模式分析） |
| **不可持续** | 每次检测升级都需要跟进适配，维护成本高 |

### 2.6 推荐方案总结

```mermaid
flowchart TD
    START{{"你想用 Coding Plan<br/>给 mnemos 省钱？"}} --> Q1{{"使用 OpenClaw？"}}

    Q1 -- 是 --> HYBRID["✅ <b>方案：插件侧 LLM</b><br/>LLM 在 OpenClaw 内调用（合规）<br/>Embedding 用 Ollama（免费）<br/>mnemo-server 仅做存储/搜索"]

    Q1 -- 否 --> Q2{{"有本地 GPU/大内存？"}}
    Q2 -- 是 --> OLLAMA["✅ <b>全本地 Ollama</b><br/>LLM: qwen3:30b-a3b<br/>Embedding: bge-m3<br/>完全免费"]
    Q2 -- 否 --> PAYGO["✅ <b>百炼按量付费</b><br/>LLM: qwen-turbo<br/>Embedding: text-embedding-v4<br/>有大量免费额度"]

    style HYBRID fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style OLLAMA fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style PAYGO fill:#fff8e1,stroke:#F9A825,stroke-width:2px
```

### 2.7 百炼费用参考

| 服务 | 模型 | 单价 | 免费额度 |
|------|------|------|---------|
| **Embedding** | text-embedding-v4 | ¥0.0005/千 Token | 100 万 Token（90 天） |
| **Embedding** | text-embedding-v3 | ¥0.0005/千 Token | 100 万 Token（90 天） |
| **LLM** | qwen-plus | ¥0.0008/千 Token（输入） | 100 万 Token |
| **LLM** | qwen-max | ¥0.004/千 Token（输入） | 100 万 Token |
| **LLM** | qwen-turbo | ¥0.0003/千 Token（输入） | 100 万 Token |

> 开通百炼后首赠 7000 万 Tokens（每个模型 100 万），足够长期个人使用。

---

## 3. Embedding 提供商对比

```mermaid
graph TD
    subgraph 云服务["<b>☁️ 云端 Embedding 服务</b>"]
        direction TB
        C1["<b>OpenAI</b><br/>text-embedding-3-small<br/>1536 维 · $0.02/M Token"]
        C2["<b>阿里云百炼</b><br/>text-embedding-v4<br/>1024 维 · ¥0.0005/千 Token<br/>100 万 Token 免费"]
        C3["<b>DeepSeek</b><br/>deepseek-embedding<br/>1024 维"]
        C4["<b>硅基流动 SiliconFlow</b><br/>bge-m3 / bge-large-zh<br/>部分模型免费"]
    end

    subgraph 本地["<b>🏠 本地 Embedding（免费）</b>"]
        direction TB
        L1["<b>Ollama</b><br/>一键安装，多模型支持"]
        L2["<b>vLLM</b><br/>高性能推理服务"]
        L3["<b>LM Studio</b><br/>GUI 管理，简单易用"]
    end

    style 云服务 fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style 本地 fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

### 3.1 云端 Embedding 服务

| 提供商 | 模型 | 维度 | 价格 | 免费额度 | 中文支持 |
|--------|------|------|------|---------|:--:|
| **OpenAI** | text-embedding-3-small | 1536 | $0.02/M Token | — | ⭐⭐⭐ |
| **OpenAI** | text-embedding-3-large | 3072 | $0.13/M Token | — | ⭐⭐⭐ |
| **阿里云百炼** | text-embedding-v4 | 64~2048 可选 | ¥0.0005/千 Token | 100 万 Token | ⭐⭐⭐⭐⭐ |
| **阿里云百炼** | text-embedding-v3 | 64~1024 可选 | ¥0.0005/千 Token | 100 万 Token | ⭐⭐⭐⭐ |
| **DeepSeek** | deepseek-embedding | 1024 | ¥0.001/千 Token | — | ⭐⭐⭐⭐ |
| **硅基流动** | BAAI/bge-m3 | 1024 | 免费 | 无限 | ⭐⭐⭐⭐⭐ |

### 3.2 本地开源 Embedding（通过 Ollama）

| 模型 | 维度 | 大小 | 语种 | 上下文 | 许可证 |
|------|------|------|------|--------|--------|
| **nomic-embed-text** | 768 | 274 MB | 英文为主 | 8192 Token | Apache 2.0 |
| **nomic-embed-text-v2-moe** | 768 | 397 MB | ~100 语种 | 8192 Token | Apache 2.0 |
| **bge-m3** | 1024 | 1.2 GB | 100+ 语种 | 8192 Token | MIT |
| **mxbai-embed-large** | 1024 | 670 MB | 英文为主 | 512 Token | Apache 2.0 |
| **snowflake-arctic-embed** | 1024 | 669 MB | 英文为主 | 512 Token | Apache 2.0 |

> **中文场景推荐**：`bge-m3`（多语言、效果好）或百炼 `text-embedding-v4`（中文最优）

### 3.3 选型决策树

```mermaid
flowchart TD
    START{{"你的 Embedding 需求"}} --> FREE{{"需要免费？"}}
    FREE -- 是 --> LOCAL{{"有 GPU？"}}
    LOCAL -- 有 --> OLLAMA["✅ <b>Ollama + bge-m3</b><br/>免费 · 本地 · 多语言<br/>1024 维"]
    LOCAL -- 无 --> SILICON["✅ <b>硅基流动 bge-m3</b><br/>免费云端 · 无 GPU 需求"]

    FREE -- 否 --> CHINESE{{"中文为主？"}}
    CHINESE -- 是 --> BAILIAN["✅ <b>百炼 text-embedding-v4</b><br/>中文最优 · 100 万免费额度<br/>可选维度 · 8K 上下文"]
    CHINESE -- 否 --> OPENAI_E["✅ <b>OpenAI text-embedding-3-small</b><br/>通用性最强 · 1536 维"]

    style OLLAMA fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style SILICON fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style BAILIAN fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style OPENAI_E fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

---

## 4. LLM 提供商对比

mnemos 中 LLM 用于两个核心功能：
1. **事实抽取**（extractFacts）：从对话中提取原子事实
2. **记忆协调**（reconcile）：比较新事实与已有记忆，决定 ADD/UPDATE/DELETE/NOOP

这两个功能对 LLM 的要求是**结构化 JSON 输出能力 + 指令遵循能力**，不需要超强的创意能力。

### 4.1 云端 LLM 服务

| 提供商 | 模型 | 输入价格 | 免费额度 | JSON 输出 | 推荐度 |
|--------|------|---------|---------|:--:|:--:|
| **阿里云百炼** | qwen-turbo | ¥0.0003/千 Token | 100 万 Token | ✅ | ⭐⭐⭐⭐⭐ |
| **阿里云百炼** | qwen-plus | ¥0.0008/千 Token | 100 万 Token | ✅ | ⭐⭐⭐⭐⭐ |
| **阿里云百炼** | qwen-max | ¥0.004/千 Token | 100 万 Token | ✅ | ⭐⭐⭐⭐ |
| **OpenAI** | gpt-4o-mini | $0.15/M Token | — | ✅ | ⭐⭐⭐⭐ |
| **OpenAI** | gpt-4o | $2.5/M Token | — | ✅ | ⭐⭐⭐ |
| **DeepSeek** | deepseek-chat | ¥0.001/千 Token | — | ✅ | ⭐⭐⭐⭐ |
| **硅基流动** | Qwen/Qwen3-8B | 免费 | 无限 | ⚠️ | ⭐⭐⭐ |

> **性价比之选**：百炼 `qwen-turbo`（最便宜 + 100 万 Token 免费）或 `qwen-plus`（更强能力）

### 4.2 本地 LLM（通过 Ollama）

| 模型 | 参数量 | 大小 | JSON 输出 | 推荐度 |
|------|--------|------|:--:|:--:|
| **qwen3:8b** | 8B | 5.2 GB | ✅ | ⭐⭐⭐⭐ |
| **qwen3:4b** | 4B | 2.6 GB | ✅ | ⭐⭐⭐ |
| **llama3.1:8b** | 8B | 4.7 GB | ✅ | ⭐⭐⭐ |
| **mistral:7b** | 7B | 4.1 GB | ⚠️ | ⭐⭐ |

> 本地 LLM 对 JSON 结构化输出的稳定性不如云端模型。mnemos 的 `CompleteJSON` 已内置了 `response_format: json_object` 不支持时的自动降级重试机制。

### 4.3 百炼 Coding Plan vs 按量付费对比

```mermaid
graph TD
    subgraph CodingPlan["<b>Coding Plan</b><br/>¥40~200/月"]
        direction TB
        CP1["✅ <b>适用于</b><br/>Claude Code 中使用 AI 编程<br/>OpenClaw 中使用 AI 编程"]
        CP2["❌ <b>不适用于</b><br/>mnemo-server 后端 API 调用<br/>自动化脚本/批量处理"]
        CP3["❌ <b>不包含</b><br/>text-embedding 模型"]
    end

    subgraph PayAsYouGo["<b>按量付费</b><br/>按 Token 计费"]
        direction TB
        PG1["✅ <b>Embedding</b><br/>text-embedding-v4<br/>100 万 Token 免费"]
        PG2["✅ <b>LLM</b><br/>qwen-turbo/plus/max<br/>各 100 万 Token 免费"]
        PG3["✅ <b>无使用场景限制</b><br/>API 调用/后端服务/自动化<br/>均允许"]
    end

    style CodingPlan fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style PayAsYouGo fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

**总结**：
- **Coding Plan** → 给你在 Claude Code / OpenClaw 里面写代码用的 AI 编程套餐，**不能用于 mnemos 后端**
- **百炼按量付费** → 可以用于 mnemos 后端的 Embedding 和 LLM，开通后有大量免费额度

---

## 5. 推荐配置方案

### 方案一：百炼全家桶（中文最优，有免费额度）

```mermaid
graph LR
    SERVER["<b>mnemo-server</b>"] -->|"Embedding"| E1["<b>百炼 text-embedding-v4</b><br/>1024 维 · 中文最优"]
    SERVER -->|"LLM"| L1["<b>百炼 qwen-plus</b><br/>事实抽取 + 记忆协调"]
    SERVER -->|"数据库"| D1["<b>PostgreSQL + pgvector</b>"]

    style SERVER fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style E1 fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
    style L1 fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
```

**适合**：中文用户、需要高质量中文 embedding、有免费额度需求

### 方案二：全本地方案（完全免费，零网络依赖）

```mermaid
graph LR
    SERVER["<b>mnemo-server</b>"] -->|"Embedding"| E2["<b>Ollama bge-m3</b><br/>1024 维 · 多语言"]
    SERVER -->|"LLM"| L2["<b>Ollama qwen3:8b</b><br/>本地推理"]
    SERVER -->|"数据库"| D2["<b>SQLite</b><br/>零依赖"]

    style SERVER fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style E2 fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
    style L2 fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
```

**适合**：离线环境、注重隐私、有 GPU 的开发者

### 方案三：混合方案（经济实惠）

```mermaid
graph LR
    SERVER["<b>mnemo-server</b>"] -->|"Embedding"| E3["<b>Ollama bge-m3</b><br/>本地免费"]
    SERVER -->|"LLM"| L3["<b>百炼 qwen-turbo</b><br/>最便宜的云端 LLM"]
    SERVER -->|"数据库"| D3["<b>PostgreSQL + pgvector</b>"]

    style SERVER fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style E3 fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
    style L3 fill:#fff8e1,stroke:#F9A825,stroke-width:1px
```

**适合**：本地 embedding 节省费用，LLM 用云端保证质量

---

## 6. 详细配置示例

### 6.1 百炼全家桶

```bash
# 数据库
MNEMO_DB_DRIVER=postgres
MNEMO_DSN="postgres://user:pass@localhost:5432/mnemos?sslmode=disable"

# Embedding：百炼 text-embedding-v4
MNEMO_EMBED_API_KEY="sk-xxxxxxxxxxxxxxxxxxxxxx"
MNEMO_EMBED_BASE_URL="https://dashscope.aliyuncs.com/compatible-mode/v1"
MNEMO_EMBED_MODEL="text-embedding-v4"
MNEMO_EMBED_DIMS=1024

# LLM：百炼 qwen-plus
MNEMO_LLM_API_KEY="sk-xxxxxxxxxxxxxxxxxxxxxx"
MNEMO_LLM_BASE_URL="https://dashscope.aliyuncs.com/compatible-mode/v1"
MNEMO_LLM_MODEL="qwen-plus"
MNEMO_LLM_TEMPERATURE=0.1

# Smart Ingest（需要 LLM）
MNEMO_INGEST_MODE=smart
```

> API Key 在 [百炼控制台](https://bailian.console.aliyun.com/) 获取，注意使用普通 API Key（`sk-` 开头），**不是** Coding Plan 的 `sk-sp-` 开头的 Key。

### 6.2 全本地 Ollama

```bash
# 1. 安装 Ollama
curl -fsSL https://ollama.com/install.sh | sh

# 2. 下载模型
ollama pull bge-m3            # Embedding 模型（1.2 GB）
ollama pull qwen3:8b          # LLM 模型（5.2 GB）

# 3. 配置 mnemo-server
MNEMO_DB_DRIVER=sqlite
MNEMO_DSN="/data/mnemos.db"

# Embedding：Ollama bge-m3
MNEMO_EMBED_BASE_URL="http://localhost:11434/v1"
MNEMO_EMBED_MODEL="bge-m3"
MNEMO_EMBED_DIMS=1024
# API Key 设为任意非空值（Ollama 不校验，但 mnemos 需要非空才启用）
MNEMO_EMBED_API_KEY="local"

# LLM：Ollama qwen3:8b
MNEMO_LLM_API_KEY="local"
MNEMO_LLM_BASE_URL="http://localhost:11434/v1"
MNEMO_LLM_MODEL="qwen3:8b"
MNEMO_LLM_TEMPERATURE=0.1

MNEMO_INGEST_MODE=smart
```

### 6.3 混合方案（Ollama Embedding + 百炼 LLM）

```bash
MNEMO_DB_DRIVER=postgres
MNEMO_DSN="postgres://user:pass@localhost:5432/mnemos?sslmode=disable"

# Embedding：本地 Ollama（免费）
MNEMO_EMBED_API_KEY="local"
MNEMO_EMBED_BASE_URL="http://localhost:11434/v1"
MNEMO_EMBED_MODEL="bge-m3"
MNEMO_EMBED_DIMS=1024

# LLM：百炼 qwen-turbo（便宜 + 效果好）
MNEMO_LLM_API_KEY="sk-xxxxxxxxxxxxxxxxxxxxxx"
MNEMO_LLM_BASE_URL="https://dashscope.aliyuncs.com/compatible-mode/v1"
MNEMO_LLM_MODEL="qwen-turbo"
MNEMO_LLM_TEMPERATURE=0.1

MNEMO_INGEST_MODE=smart
```

### 6.4 OpenAI 方案

```bash
MNEMO_DB_DRIVER=postgres
MNEMO_DSN="postgres://user:pass@localhost:5432/mnemos?sslmode=disable"

# Embedding + LLM 使用同一个 OpenAI Key
MNEMO_EMBED_API_KEY="sk-proj-xxxxxx"
MNEMO_EMBED_MODEL="text-embedding-3-small"
MNEMO_EMBED_DIMS=1536

MNEMO_LLM_API_KEY="sk-proj-xxxxxx"
MNEMO_LLM_MODEL="gpt-4o-mini"

MNEMO_INGEST_MODE=smart
```

### 6.5 硅基流动方案（完全免费云端）

```bash
MNEMO_DB_DRIVER=sqlite
MNEMO_DSN="/data/mnemos.db"

# Embedding：硅基流动 bge-m3（免费）
MNEMO_EMBED_API_KEY="sk-xxxxxx"
MNEMO_EMBED_BASE_URL="https://api.siliconflow.cn/v1"
MNEMO_EMBED_MODEL="BAAI/bge-m3"
MNEMO_EMBED_DIMS=1024

# LLM：硅基流动 Qwen3-8B（免费）
MNEMO_LLM_API_KEY="sk-xxxxxx"
MNEMO_LLM_BASE_URL="https://api.siliconflow.cn/v1"
MNEMO_LLM_MODEL="Qwen/Qwen3-8B"
MNEMO_LLM_TEMPERATURE=0.1

MNEMO_INGEST_MODE=smart
```

> 硅基流动注册后免费提供部分开源模型的推理服务，适合预算为零的场景。

---

## 附录 A：本地小型主机部署方案（以铭帆 UM890 Pro 为例）

### 硬件分析

| 配置项 | 规格 | 对 Ollama 的意义 |
|--------|------|-----------------|
| **CPU** | AMD Ryzen 9 8945HS（8C/16T, Zen 4, 5.2GHz） | CPU 推理主力，DDR5 内存控制器加速读取模型权重 |
| **内存** | 96GB DDR5 | 远超大多数模型需求，可同时加载 LLM + Embedding 模型 |
| **SSD** | 3TB NVMe | 模型加载秒级完成，存储空间充裕 |
| **iGPU** | Radeon 780M（RDNA 3, 12 CU） | Ollama 对 780M 支持不稳定，**建议按 CPU 推理规划** |

> **关于 780M 核显**：Ollama 官方尚未完全支持 gfx1103 架构。虽然可以通过 `HSA_OVERRIDE_GFX_VERSION=11.0.2` 环境变量尝试启用，但存在兼容性和稳定性问题。建议以 CPU 推理为主，核显加速作为可选尝试。

### 推荐模型组合

```mermaid
graph TD
    subgraph UM890["<b>铭帆 UM890 Pro（96GB DDR5）</b>"]
        direction TB
        OLLAMA["<b>Ollama 运行时</b>"]
        EMB_MODEL["<b>bge-m3</b><br/>Embedding 模型<br/>1.2GB · 1024 维"]
        LLM_MODEL["<b>qwen3:30b-a3b</b><br/>LLM 模型（MoE）<br/>~18GB · 仅 3B 激活参数<br/>~22 tok/s"]
        OLLAMA --> EMB_MODEL
        OLLAMA --> LLM_MODEL
    end

    subgraph 内存分配["<b>96GB DDR5 内存分配</b>"]
        direction TB
        M1["<b>系统</b><br/>~4GB"]
        M2["<b>bge-m3</b><br/>~1.5GB"]
        M3["<b>qwen3:30b-a3b</b><br/>~18GB"]
        M4["<b>mnemo-server + PG</b><br/>~2GB"]
        M5["<b>剩余可用</b><br/>~70GB"]
    end

    style UM890 fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style 内存分配 fill:#f3e5f5,stroke:#7B1FA2,stroke-width:2px
    style M5 fill:#e8f5e9,stroke:#2E7D32,stroke-width:1px
```

### 模型选型详解

#### Embedding 模型：bge-m3（唯一推荐）

| 属性 | 值 |
|------|-----|
| 模型 | `bge-m3` |
| 大小 | 1.2 GB |
| 维度 | 1024 |
| 语种 | 100+ 语种（中英文优秀） |
| 上下文 | 8,192 Token |
| 许可证 | MIT |
| CPU 推理速度 | 极快（embedding 模型计算量远小于生成模型） |

```bash
ollama pull bge-m3
```

> bge-m3 是当前开源 embedding 中综合能力最强的模型之一，尤其中文效果接近百炼 text-embedding-v4。

#### LLM 模型对比

| 模型 | 参数量 | 激活参数 | 内存占用 | 预估速度 | JSON 输出 | 推荐度 |
|------|--------|---------|---------|---------|:--:|:--:|
| **qwen3:30b-a3b** | 30B（MoE） | **3B** | ~18GB | **~22 tok/s** | ✅ 优秀 | ⭐⭐⭐⭐⭐ |
| **qwen3:14b** | 14B（Dense） | 14B | ~9GB | ~10-15 tok/s | ✅ 优秀 | ⭐⭐⭐⭐ |
| **qwen3:8b** | 8B（Dense） | 8B | ~5GB | ~18-25 tok/s | ✅ 良好 | ⭐⭐⭐ |
| **qwen3:32b** | 32B（Dense） | 32B | ~20GB | ~5-8 tok/s | ✅ 最优 | ⭐⭐⭐ |

**首选推荐：`qwen3:30b-a3b`**

```mermaid
graph LR
    subgraph MoE["<b>MoE 架构优势</b>"]
        direction TB
        A["<b>30B 总参数</b><br/>= 30B 级别的知识量"]
        B["<b>每次仅激活 3B</b><br/>= 8B 级别的推理速度"]
        C["<b>结果</b><br/>30B 的质量 + 8B 的速度"]
        A --> C
        B --> C
    end

    style MoE fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

为什么选 `qwen3:30b-a3b` 而不是其他模型：

1. **速度远超同质量模型**：MoE 架构每次推理只激活 3B 参数，在同代 Ryzen 8845HS 上实测 ~22 tok/s，比 dense 14B 快近一倍
2. **质量远超同速度模型**：拥有 30B 的知识存储，JSON 结构化输出能力显著优于 8B
3. **内存充裕**：~18GB 对 96GB 内存毫无压力，还能同时跑 bge-m3 + PostgreSQL
4. **适合 mnemos 场景**：事实抽取和记忆协调需要准确的 JSON 输出，30B 级别的指令遵循能力是保障

```bash
ollama pull qwen3:30b-a3b
```

### 一键部署脚本

```bash
#!/bin/bash
# mnemos 本地部署一键脚本（铭帆 UM890 Pro / 类似配置）

echo "=== 1. 安装 Ollama ==="
curl -fsSL https://ollama.com/install.sh | sh

echo "=== 2. 下载模型 ==="
ollama pull bge-m3           # Embedding（1.2GB）
ollama pull qwen3:30b-a3b    # LLM - MoE（~18GB）

echo "=== 3. 验证模型 ==="
echo "测试 Embedding..."
curl -s http://localhost:11434/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{"model":"bge-m3","input":"测试中文向量化"}' | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(f'  维度: {len(d[\"data\"][0][\"embedding\"])}')
print('  Embedding ✅')
"

echo "测试 LLM..."
curl -s http://localhost:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model":"qwen3:30b-a3b",
    "messages":[{"role":"user","content":"返回 JSON: {\"status\":\"ok\"}"}],
    "temperature":0.1
  }' | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(f'  回复: {d[\"choices\"][0][\"message\"][\"content\"][:100]}')
print('  LLM ✅')
"

echo ""
echo "=== 4. mnemo-server 配置 ==="
cat << 'ENVEOF'
# 复制以下环境变量到 mnemo-server 启动配置中：

MNEMO_DB_DRIVER=postgres
MNEMO_DSN="postgres://mnemos:mnemos@localhost:5432/mnemos?sslmode=disable"

# Embedding：Ollama bge-m3
MNEMO_EMBED_API_KEY="local"
MNEMO_EMBED_BASE_URL="http://localhost:11434/v1"
MNEMO_EMBED_MODEL="bge-m3"
MNEMO_EMBED_DIMS=1024

# LLM：Ollama qwen3:30b-a3b
MNEMO_LLM_API_KEY="local"
MNEMO_LLM_BASE_URL="http://localhost:11434/v1"
MNEMO_LLM_MODEL="qwen3:30b-a3b"
MNEMO_LLM_TEMPERATURE=0.1

MNEMO_INGEST_MODE=smart
MNEMO_FTS_ENABLED=true
ENVEOF

echo ""
echo "=== 部署完成 ==="
echo "模型总占用：~19GB / 96GB 可用内存"
echo "预估 LLM 速度：~22 tok/s（CPU 推理）"
echo "预估 Embedding 速度：<100ms/条"
```

### 性能预估

| 操作 | 预估耗时 | 说明 |
|------|---------|------|
| **单次 Embedding** | < 100ms | bge-m3 在 CPU 上极快 |
| **事实抽取**（extractFacts） | 3-8 秒 | 一次 LLM 调用，~200-500 token 输出 |
| **记忆协调**（reconcile） | 5-15 秒 | 一次 LLM 调用，包含 existing memories 上下文 |
| **完整 Ingest 管道** | 10-30 秒 | 抽取 + 搜索 + 协调 + 写入 |
| **向量搜索** | < 200ms | Embedding query + PG 查询 |

> mnemos 的 LLM 调用是后台异步处理（Ingest 管道），用户不会感知延迟。22 tok/s 的速度完全满足需求。

### 可选：尝试 780M 核显加速

如果想尝试利用核显加速（可能进一步提速 30-50%，但不保证稳定）：

```bash
# Linux 下尝试启用 780M（不稳定，如遇问题可回退到纯 CPU）
export HSA_OVERRIDE_GFX_VERSION=11.0.2
export OLLAMA_GPU_OVERHEAD=0
systemctl restart ollama

# 验证是否使用了 GPU
ollama ps
# 如果 PROCESSOR 列显示 GPU，说明核显已启用
```

---

## 附录 B：百炼 Embedding 模型详细参数

### text-embedding-v4（推荐）

| 属性 | 值 |
|------|-----|
| 模型系列 | Qwen3-Embedding |
| 维度选择 | 64 / 128 / 256 / 512 / 768 / 1024（默认）/ 1536 / 2048 |
| 最大输入 | 8,192 Token |
| 批量大小 | 最多 10 条/次 |
| 语种支持 | 100+ 语种 + 多种编程语言 |
| 价格 | ¥0.0005/千 Token |
| 免费额度 | 100 万 Token（开通后 90 天内） |

### 调用示例（cURL）

```bash
curl 'https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings' \
  -H "Authorization: Bearer $DASHSCOPE_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "text-embedding-v4",
    "input": "mnemos 是 AI Agent 的长期记忆系统",
    "encoding_format": "float",
    "dimensions": 1024
  }'
```

响应格式与 OpenAI 完全兼容，mnemos 的 `embed.Embedder` 无需任何修改即可使用。
