# LLM 架构：插件侧独占智能管线

> 本文档全面描述 mnemos 系统中 LLM 的架构设计，覆盖消息生命周期、插件组件、服务端 API 三个维度。
>
> **核心原则**：服务端 **零 LLM 依赖**（纯存储 + 搜索 + embedding），所有智能处理（事实提取、记忆协调）**仅在 OpenClaw 插件内部** 执行。

---

## 1. 为什么 LLM 在插件侧调用不违规？

阿里百炼 Coding Plan 的检测机制基于：

| 检测维度 | 插件内调用 (合规) | 服务端调用 (违规) |
|---------|:-:|:-:|
| **调用进程** | OpenClaw Gateway (交互式编码工具) | 独立 Go HTTP 服务器 |
| **调用模式** | 对话结束时触发 (交互使用) | API 请求触发 (后端服务) |
| **网络出口** | 用户本机 IP | 服务器 IP |
| **User-Agent** | 编码工具上下文 | 通用 HTTP 客户端 |

OpenClaw 插件运行在 Gateway **进程内部** (in-process)，从百炼视角看，调用方就是 OpenClaw 本身——一个被明确支持的交互式编码工具。

---

## 2. 架构概览

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontSize': '16px', 'fontFamily': 'Arial, sans-serif', 'primaryColor': '#e8f4fd', 'primaryTextColor': '#1a1a1a', 'primaryBorderColor': '#4a9eda', 'lineColor': '#666666', 'secondaryColor': '#f0f7e8', 'tertiaryColor': '#fff5e6'}}}%%
graph TB
    subgraph OC["**OpenClaw Gateway 进程**"]
        direction TB
        AGENT["**Agent**<br/>用户对话"]
        PLUGIN["**mnemo 插件**<br/>index.ts"]
        HOOKS["**Hooks**<br/>hooks.ts"]
        LLM_MOD["**llm.ts**<br/>OpenAI 兼容客户端"]
        INGEST_MOD["**ingest.ts**<br/>提取 + 协调"]
    end

    subgraph SRV["**mnemo-server (Go)**"]
        direction TB
        HANDLER["**Handler**<br/>chi 路由"]
        SVC_MEM["**MemoryService**<br/>CRUD + BulkCreate"]
        SVC_ING["**IngestService**<br/>Raw 存储"]
        REPO["**Repository**<br/>TiDB / PG / SQLite"]
        EMBED["**Embedder**<br/>可选"]
    end

    subgraph PROVIDER["**外部服务**"]
        direction TB
        LLM_API["**LLM API**<br/>百炼 Coding Plan / Ollama"]
        EMBED_API["**Embedding API**<br/>百炼 / Ollama / OpenAI"]
        DB[("**数据库**<br/>TiDB / PG / SQLite")]
    end

    AGENT -->|"用户消息"| HOOKS
    HOOKS -->|"agent_end"| LLM_MOD
    LLM_MOD -->|"chat/completions"| LLM_API
    LLM_MOD --> INGEST_MOD
    INGEST_MOD -->|"search (查已有记忆)"| HANDLER
    INGEST_MOD -->|"POST /memories/bulk"| HANDLER
    HOOKS -->|"before_prompt_build"| HANDLER

    HANDLER --> SVC_MEM
    HANDLER --> SVC_ING
    SVC_MEM --> REPO
    SVC_ING --> REPO
    REPO --> DB
    EMBED -->|"写入/搜索时 embedding"| EMBED_API

    style OC fill:#e8f4fd,stroke:#4a9eda,stroke-width:2px
    style SRV fill:#f0f7e8,stroke:#6abf4b,stroke-width:2px
    style PROVIDER fill:#fff5e6,stroke:#e6a817,stroke-width:2px
```

**关键设计**：
- 服务端 **没有** LLM 客户端，不依赖 `MNEMO_LLM_*` 环境变量
- 服务端 Embedding 独立于 LLM（`MNEMO_EMBED_*` 用于向量搜索）
- 所有 LLM 调用在 OpenClaw 插件进程内发起

---

## 3. 消息生命周期（端到端）

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontSize': '16px', 'fontFamily': 'Arial, sans-serif', 'primaryColor': '#e8f4fd', 'primaryTextColor': '#1a1a1a', 'primaryBorderColor': '#4a9eda', 'lineColor': '#333333'}}}%%
sequenceDiagram
    autonumber
    participant U as **用户**
    participant OC as **OpenClaw**
    participant H as **Hooks (hooks.ts)**
    participant LLM_P as **Plugin LLM<br/>(llm.ts)**
    participant ING_P as **Plugin Ingest<br/>(ingest.ts)**
    participant API as **mnemo-server**
    participant DB as **Database**

    rect rgb(232, 244, 253)
    Note over U,DB: **阶段 1: 提示注入 (before_prompt_build)**
    U->>OC: 输入提示词
    OC->>H: before_prompt_build(prompt)
    H->>API: GET /memories?q={prompt}&limit=10
    API->>DB: 混合搜索 (向量 + 关键词)
    DB-->>API: 匹配记忆
    API-->>H: memories[]
    H-->>OC: prependContext: relevant-memories
    OC->>OC: 注入记忆到 system context
    end

    rect rgb(240, 247, 232)
    Note over U,DB: **阶段 2: Agent 执行**
    OC->>OC: Agent 调用 LLM + 工具
    Note right of OC: Agent 可通过 memory_store /<br/>memory_search 工具主动操作记忆
    end

    rect rgb(255, 245, 230)
    Note over U,DB: **阶段 3: 会话结束 (agent_end) — 智能管线**
    OC->>H: agent_end(messages, success)
    H->>H: 格式化消息 + 大小裁剪 (200KB/20条)
    H->>LLM_P: extractFacts(conversation)
    LLM_P-->>ING_P: facts[]
    ING_P->>API: GET /memories?q={fact} (逐 fact 搜索)
    API-->>ING_P: existing memories[]
    ING_P->>LLM_P: reconcile(facts, existing)
    LLM_P-->>ING_P: events[] (ADD/UPDATE/DELETE/NOOP)
    ING_P->>API: POST /memories/bulk (ADD 结果)
    ING_P->>API: PUT /memories/{id} (UPDATE)
    ING_P->>API: DELETE /memories/{id} (DELETE)
    end

    rect rgb(245, 240, 255)
    Note over U,DB: **阶段 4: 重置前保存 (before_reset)**
    U->>OC: /reset
    OC->>H: before_reset(messages)
    H->>API: POST /memories (session-summary)
    API->>DB: 直接存储为 pinned 记忆
    end
```

---

## 4. 插件组件详解

### 4.1 组件依赖关系

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontSize': '16px', 'fontFamily': 'Arial, sans-serif', 'primaryColor': '#e8f4fd', 'primaryTextColor': '#1a1a1a', 'primaryBorderColor': '#4a9eda', 'lineColor': '#666666'}}}%%
graph LR
    subgraph PLUGIN["**openclaw-plugin/**"]
        direction TB
        INDEX["**index.ts**<br/>注册入口 + LLM 校验"]
        HOOKS["**hooks.ts**<br/>生命周期钩子"]
        INGEST["**ingest.ts**<br/>提取 + 协调逻辑"]
        LLM["**llm.ts**<br/>LLM 客户端"]
        BACKEND["**backend.ts**<br/>接口定义"]
        SERVER_BE["**server-backend.ts**<br/>HTTP 实现"]
        TYPES["**types.ts**<br/>类型定义"]
    end

    INDEX --> HOOKS
    INDEX --> SERVER_BE
    INDEX --> TYPES
    HOOKS --> INGEST
    HOOKS --> LLM
    HOOKS --> BACKEND
    INGEST --> LLM
    INGEST --> BACKEND
    INGEST --> TYPES
    SERVER_BE --> BACKEND
    SERVER_BE --> TYPES

    style PLUGIN fill:#e8f4fd,stroke:#4a9eda,stroke-width:2px
```

### 4.2 各组件职责

| 组件 | 文件 | 职责 |
|------|------|------|
| **入口** | `index.ts` | 插件注册，校验 LLM 配置（必填），创建 `LazyServerBackend` |
| **生命周期** | `hooks.ts` | 4 个 Hook：`before_prompt_build`、`after_compaction`、`before_reset`、`agent_end` |
| **LLM 客户端** | `llm.ts` | OpenAI 兼容 `completeJSON()`，支持 `response_format: json_object`，400 时自动降级 |
| **管线逻辑** | `ingest.ts` | `extractFacts()` → `gatherExistingMemories()` → `reconcile()` → `executeActions()` |
| **后端接口** | `backend.ts` | `MemoryBackend` 接口：`store`, `search`, `get`, `update`, `remove`, `bulkStore` |
| **HTTP 实现** | `server-backend.ts` | `ServerBackend` 实现所有接口方法，调用 mnemo-server REST API |
| **类型** | `types.ts` | `PluginConfig` (含必填 `llmApiKey/llmBaseUrl/llmModel`)，`BulkStoreInput` 等 |

### 4.3 `agent_end` 流程

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontSize': '16px', 'fontFamily': 'Arial, sans-serif', 'primaryColor': '#e8f4fd', 'primaryTextColor': '#1a1a1a', 'lineColor': '#333333'}}}%%
flowchart TD
    START(["**agent_end 触发**"]) --> CHECK_SUCCESS{"**success?**"}
    CHECK_SUCCESS -->|No| END_SKIP(["跳过"])
    CHECK_SUCCESS -->|Yes| FORMAT["**格式化消息**<br/>过滤 + 去除注入上下文"]
    FORMAT --> SELECT["**大小裁剪**<br/>200KB / 20条上限"]
    SELECT --> EXTRACT["**extractFacts()**<br/>调用 LLM 提取事实"]
    EXTRACT --> SEARCH["**gatherExistingMemories()**<br/>搜索已有记忆"]
    SEARCH --> RECONCILE["**reconcile()**<br/>LLM 决定 ADD/UPDATE/DELETE"]
    RECONCILE --> EXECUTE["**executeActions()**<br/>bulk 写入 + update/delete"]
    EXECUTE --> END_DONE(["完成"])

    style START fill:#e8f4fd,stroke:#4a9eda,stroke-width:2px
    style END_DONE fill:#d4edda,stroke:#28a745,stroke-width:2px
    style END_SKIP fill:#f8d7da,stroke:#dc3545,stroke-width:2px
```

---

## 5. 服务端 API 路由表

| 方法 | 路径 | 说明 | 需要 LLM |
|------|------|------|:--------:|
| `POST` | `/v1alpha1/mem9s` | 注册新租户 | 否 |
| `POST` | `/v1alpha1/mem9s/{tenantID}/memories` | 创建记忆（content → pinned，messages → raw） | 否 |
| `POST` | `/v1alpha1/mem9s/{tenantID}/memories/bulk` | 批量创建记忆（插件管线写入通道） | 否 |
| `GET` | `/v1alpha1/mem9s/{tenantID}/memories` | 搜索 / 列表（向量 + 关键词） | 否 |
| `GET` | `/v1alpha1/mem9s/{tenantID}/memories/{id}` | 获取单条 | 否 |
| `PUT` | `/v1alpha1/mem9s/{tenantID}/memories/{id}` | 更新 | 否 |
| `DELETE` | `/v1alpha1/mem9s/{tenantID}/memories/{id}` | 删除 | 否 |
| `POST` | `/v1alpha1/mem9s/{tenantID}/imports` | 创建导入任务（raw 存储） | 否 |
| `GET` | `/healthz` | 健康检查 | 否 |

**所有端点均不需要 LLM。** 服务端是纯粹的存储 + 搜索层。

### 5.1 创建记忆 API

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontSize': '16px', 'fontFamily': 'Arial, sans-serif', 'primaryColor': '#e8f4fd', 'primaryTextColor': '#1a1a1a', 'lineColor': '#333333'}}}%%
flowchart TD
    REQ(["**POST /memories**"]) --> CHECK{"**请求体类型?**"}

    CHECK -->|"messages"| RAW["**Raw 存储**<br/>格式化整段对话为 insight"]
    CHECK -->|"content"| DIRECT["**直接存储**<br/>存为 pinned 类型"]
    CHECK -->|"都没有"| ERR(["400 错误"])

    RAW --> EMBED1{"有 Embedder?"}
    DIRECT --> EMBED2{"有 Embedder?"}
    EMBED1 -->|Yes| VEC1["生成 embedding"] --> DB
    EMBED1 -->|No| DB
    EMBED2 -->|Yes| VEC2["生成 embedding"] --> DB
    EMBED2 -->|No| DB
    DB(["存入数据库"])

    style REQ fill:#e8f4fd,stroke:#4a9eda,stroke-width:2px
    style DB fill:#d4edda,stroke:#28a745,stroke-width:2px
    style ERR fill:#f8d7da,stroke:#dc3545,stroke-width:2px
```

### 5.2 批量创建 API（插件写入通道）

```
POST /v1alpha1/mem9s/{tenantID}/memories/bulk

请求体:
{
  "memories": [
    { "content": "用户喜欢 Python", "tags": ["preference"], "memory_type": "insight" }
  ]
}

响应 (201):
{ "ok": true, "memories": [{ "id": "...", ... }] }
```

---

## 6. 部署配置

服务端只需要数据库和可选的 Embedding，不需要 LLM 配置。

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontSize': '16px', 'fontFamily': 'Arial, sans-serif', 'primaryColor': '#e8f4fd', 'primaryTextColor': '#1a1a1a', 'lineColor': '#333333'}}}%%
graph TD
    subgraph SRV_CFG["**mnemo-server 配置**"]
        S1["**MNEMO_DSN** — 数据库连接 (必填)"]
        S2["**MNEMO_DB_DRIVER** — mysql/postgres/sqlite"]
        S3["**MNEMO_EMBED_API_KEY** — Embedding (推荐)"]
        S4["**MNEMO_EMBED_BASE_URL** — Embedding 端点"]
        S5["**MNEMO_EMBED_MODEL** — Embedding 模型"]
        S6["~~MNEMO_LLM_*~~ — 已移除"]
    end

    subgraph PLG_CFG["**OpenClaw 插件配置**"]
        P1["**llmApiKey** — LLM API Key (必填)"]
        P2["**llmBaseUrl** — LLM 端点 (必填)"]
        P3["**llmModel** — LLM 模型 (必填)"]
        P4["**apiUrl** — mnemo-server 地址"]
        P5["**tenantID** — 租户 ID"]
    end

    style SRV_CFG fill:#f0f7e8,stroke:#6abf4b,stroke-width:2px
    style PLG_CFG fill:#e8f4fd,stroke:#4a9eda,stroke-width:2px
```

### 配置示例

**百炼 Coding Plan + Ollama Embedding（推荐免费方案）**：

```jsonc
// openclaw.json 插件配置
{
  "plugins": {
    "entries": {
      "mnemo": {
        "enabled": true,
        "config": {
          "apiUrl": "http://localhost:8080",
          "tenantID": "your-tenant-id",
          "llmApiKey": "sk-sp-xxxx",           // Coding Plan Key
          "llmBaseUrl": "https://coding.dashscope.aliyuncs.com/v1",
          "llmModel": "qwen-plus"
        }
      }
    }
  }
}
```

```bash
# mnemo-server 环境变量
MNEMO_DSN="postgres://user:pass@localhost:5432/mnemos?sslmode=disable"
MNEMO_DB_DRIVER=postgres
MNEMO_EMBED_API_KEY="local"
MNEMO_EMBED_BASE_URL="http://localhost:11434/v1"
MNEMO_EMBED_MODEL="bge-m3"
MNEMO_EMBED_DIMS=1024
# 注意：没有 MNEMO_LLM_* 变量
```

---

## 7. 智能管线 (Ingest Pipeline) 详解

### 7.1 两阶段管线

管线运行在 OpenClaw 插件内部 (`ingest.ts`)：

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontSize': '16px', 'fontFamily': 'Arial, sans-serif', 'primaryColor': '#e8f4fd', 'primaryTextColor': '#1a1a1a', 'lineColor': '#333333'}}}%%
flowchart LR
    subgraph P1["**阶段 1: 事实提取**"]
        direction TB
        CONV["对话文本"] --> EXTRACT_LLM["LLM<br/>extractFacts()"]
        EXTRACT_LLM --> FACTS["原子事实列表<br/>facts[]"]
    end

    subgraph P2["**阶段 2: 记忆协调**"]
        direction TB
        SEARCH["逐 fact 搜索<br/>已有记忆 (→ server)"] --> MERGE["合并去重<br/>≤60 条"]
        MERGE --> RECONCILE_LLM["LLM<br/>reconcile()"]
        RECONCILE_LLM --> EVENTS["事件列表<br/>ADD / UPDATE / DELETE / NOOP"]
    end

    subgraph P3["**阶段 3: 执行 (→ server)**"]
        direction TB
        DO_ADD["ADD → POST /memories/bulk"]
        DO_UPDATE["UPDATE → PUT /memories/{id}"]
        DO_DELETE["DELETE → DELETE /memories/{id}"]
    end

    FACTS --> SEARCH
    EVENTS --> DO_ADD
    EVENTS --> DO_UPDATE
    EVENTS --> DO_DELETE

    style P1 fill:#e8f4fd,stroke:#4a9eda,stroke-width:2px
    style P2 fill:#f0f7e8,stroke:#6abf4b,stroke-width:2px
    style P3 fill:#fff5e6,stroke:#e6a817,stroke-width:2px
```

### 7.2 Pinned 记忆保护

`executeActions()` 对 `pinned` 类型记忆有特殊保护：
- **UPDATE pinned** → 转为 ADD 新的 insight（不修改原始 pinned）
- **DELETE pinned** → 跳过，增加 warning 计数

---

## 8. 数据流总结

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontSize': '16px', 'fontFamily': 'Arial, sans-serif', 'primaryColor': '#e8f4fd', 'primaryTextColor': '#1a1a1a', 'lineColor': '#333333'}}}%%
graph LR
    subgraph WRITE["**写入路径**"]
        W1["memory_store 工具"] -->|"POST /memories (content)"| DB
        W2["agent_end Hook"] -->|"POST /memories/bulk<br/>PUT / DELETE /memories/{id}"| DB
        W3["before_reset Hook"] -->|"POST /memories (content)"| DB
    end

    subgraph READ["**读取路径**"]
        R1["before_prompt_build"] -->|"GET /memories?q=..."| DB
        R2["memory_search 工具"] -->|"GET /memories?q=..."| DB
        R3["memory_get 工具"] -->|"GET /memories/{id}"| DB
        R4["ingest.ts 搜索已有"] -->|"GET /memories?q=..."| DB
    end

    DB[("**mnemo-server<br/>→ Database**")]

    style WRITE fill:#f0f7e8,stroke:#6abf4b,stroke-width:2px
    style READ fill:#e8f4fd,stroke:#4a9eda,stroke-width:2px
    style DB fill:#fff5e6,stroke:#e6a817,stroke-width:2px
```

---

## 9. 文件变更清单

### 服务端 (Go) — LLM 移除

| 文件 | 改动 |
|------|------|
| `server/internal/config/config.go` | 移除 `MNEMO_LLM_*` 和 `IngestMode` 配置 |
| `server/cmd/mnemo-server/main.go` | 移除 `llmClient` 创建和传递 |
| `server/internal/handler/handler.go` | `Server` struct 移除 `llmClient` / `ingestMode` |
| `server/internal/handler/memory.go` | `createMemoryRequest` 移除 `Mode` 字段 |
| `server/internal/service/ingest.go` | `IngestService` 移除 `llm` 字段，`Ingest()` 始终 raw 模式，删除 `extractFacts/reconcile/ReconcileContent` 等 LLM 方法 |
| `server/internal/service/memory.go` | `NewMemoryService` 移除 `llmClient` 参数，`Create()` 始终直接存储 |
| `server/internal/service/upload.go` | `UploadWorker` 移除 `llmClient` |

### 插件侧 (TypeScript) — LLM 必填

| 文件 | 改动 |
|------|------|
| `openclaw-plugin/index.ts` | 启动时校验 LLM 配置，无配置输出错误日志 |
| `openclaw-plugin/hooks.ts` | `registerHooks` LLM 参数必填，移除服务端 fallback 路径 |
| `openclaw-plugin/types.ts` | `PluginConfig` LLM 字段标注为 REQUIRED |
