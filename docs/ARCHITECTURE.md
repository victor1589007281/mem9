# Mnemos 架构与实现原理分析

> 基于源码分析的技术总结，涵盖长期记忆存储、混合搜索、OpenClaw 集成及 MySQL 兼容性。

---

## 目录

- [1. 系统总览](#1-系统总览)
- [2. 长期记忆如何保存](#2-长期记忆如何保存)
  - [2.1 智能 Ingest 管道](#21-智能-ingest-管道)
  - [2.2 事实抽取（Phase 1）](#22-事实抽取phase-1)
  - [2.3 记忆协调（Phase 2）](#23-记忆协调phase-2)
  - [2.4 版本控制与冲突解决](#24-版本控制与冲突解决)
- [3. 搜索如何工作](#3-搜索如何工作)
  - [3.1 搜索策略选择](#31-搜索策略选择)
  - [3.2 混合搜索算法（RRF）](#32-混合搜索算法rrf)
  - [3.3 向量搜索细节](#33-向量搜索细节)
  - [3.4 关键词搜索细节](#34-关键词搜索细节)
- [4. OpenClaw 集成使用指南](#4-openclaw-集成使用指南)
  - [4.1 插件架构](#41-插件架构)
  - [4.2 自动记忆生命周期](#42-自动记忆生命周期)
  - [4.3 安装与配置](#43-安装与配置)
- [5. 本地 MySQL 兼容性分析](#5-本地-mysql-兼容性分析)
- [6. 数据库 Schema](#6-数据库-schema)
- [7. 核心源码索引](#7-核心源码索引)

---

## 1. 系统总览

Mnemos 是 AI Agent 的**云持久化记忆系统**。核心理念：记忆应该是**自动的**，而非依赖 Agent 自行判断何时存取。

```mermaid
graph TB
    subgraph Agents["<b>AI Agent 客户端</b>"]
        OC["<b>OpenClaw Plugin</b><br/>TypeScript"]
        CC["<b>Claude Code Plugin</b><br/>Bash Hooks"]
        OCode["<b>OpenCode Plugin</b><br/>TypeScript"]
        ANY["<b>任意 HTTP 客户端</b><br/>curl / fetch"]
    end

    subgraph Server["<b>mnemo-server (Go)</b>"]
        direction TB
        MW["<b>中间件层</b><br/>Tenant 解析 · 限流"]
        HL["<b>Handler 层</b><br/>HTTP 路由 · 请求处理"]
        SV["<b>Service 层</b><br/>记忆 CRUD · 混合搜索<br/>智能 Ingest 管道"]
        RP["<b>Repository 层</b><br/>SQL 查询 · 向量搜索"]
        EM["<b>Embed 模块</b><br/>OpenAI / Ollama"]
        LM["<b>LLM 模块</b><br/>事实抽取 · 记忆协调"]
    end

    subgraph DB["<b>数据层</b>"]
        TIDB["<b>TiDB Serverless</b><br/>VECTOR + FTS + SQL"]
        MYSQL["<b>MySQL</b><br/>SQL（关键词搜索）"]
    end

    OC --> HL
    CC --> HL
    OCode --> HL
    ANY --> HL
    MW --> HL
    HL --> SV
    SV --> RP
    SV --> EM
    SV --> LM
    RP --> TIDB
    RP --> MYSQL

    style Agents fill:#e8f4f8,stroke:#2196F3,stroke-width:2px
    style Server fill:#fff3e0,stroke:#FF9800,stroke-width:2px
    style DB fill:#e8f5e9,stroke:#4CAF50,stroke-width:2px
```

**分层架构**：`Handler → Service → Repository`，所有依赖通过接口注入，在 `main.go` 中手动组装（无框架 DI）。

**多租户隔离**：每个租户有独立的 TiDB Serverless 数据库实例，通过 URL 路径中的 `{tenantID}` 识别，中间件负责解析租户并从连接池获取对应的数据库连接。

---

## 2. 长期记忆如何保存

### 2.1 智能 Ingest 管道

Mnemos 的核心创新在于**两阶段智能 Ingest 管道**：不是简单地存储对话原文，而是通过 LLM 抽取原子事实并与已有记忆进行协调，避免重复和矛盾。

```mermaid
flowchart TD
    START(["<b>对话结束 / 会话 Reset</b>"]) --> STRIP["<b>清洗消息</b><br/>移除已注入的 relevant-memories 标签"]
    STRIP --> FORMAT["<b>格式化对话</b><br/>Role: Content 格式拼接"]
    FORMAT --> EXTRACT["<b>Phase 1: 事实抽取</b><br/>LLM 从用户消息中<br/>提取原子事实"]
    EXTRACT --> FACTS{{"提取到事实？"}}
    FACTS -- 无 --> DONE_EMPTY(["<b>完成</b><br/>无新记忆"])
    FACTS -- 有 --> GATHER["<b>搜索已有记忆</b><br/>对每个事实做<br/>向量 + 关键词搜索"]
    GATHER --> HAS_MEM{{"找到相关记忆？"}}
    HAS_MEM -- 无 --> ADD_ALL["<b>全部 ADD</b><br/>所有事实作为新 insight 写入"]
    HAS_MEM -- 有 --> RECONCILE["<b>Phase 2: 记忆协调</b><br/>单次 LLM 调用<br/>决定 ADD / UPDATE / DELETE / NOOP"]
    RECONCILE --> EXEC["<b>执行动作</b>"]
    EXEC --> ADD["<b>ADD</b><br/>创建新 insight 记忆"]
    EXEC --> UPDATE["<b>UPDATE</b><br/>归档旧记忆 + 创建新记忆"]
    EXEC --> DELETE["<b>DELETE</b><br/>软删除（state → deleted）"]
    EXEC --> NOOP["<b>NOOP</b><br/>已存在，无需操作"]
    ADD --> DONE(["<b>完成</b>"])
    UPDATE --> DONE
    DELETE --> DONE
    NOOP --> DONE
    ADD_ALL --> DONE

    style START fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style EXTRACT fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style RECONCILE fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style DONE fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style DONE_EMPTY fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

**源码路径**：`server/internal/service/ingest.go` — `Ingest()` 方法

### 2.2 事实抽取（Phase 1）

LLM 从对话中提取**原子事实**（每个事实一个独立的、自包含的陈述）：

- 仅从**用户消息**中提取，忽略 assistant 和 system 消息
- 保留用户原始语言（中文输入提取中文事实）
- 过滤掉临时性信息（问候语、调试聊天等）
- 单次对话最多提取 50 个事实

**返回格式**：`{"facts": ["事实一", "事实二", ...]}`

**源码路径**：`server/internal/service/ingest.go` — `extractFacts()` 方法

### 2.3 记忆协调（Phase 2）

协调阶段是避免记忆**膨胀和矛盾**的关键：

1. **搜索已有记忆**：对每个新事实分别做向量搜索 + 关键词搜索（每个事实最多 5 条），去重后上限 60 条
2. **ID 映射**：将真实 UUID 映射为整数 ID（`0, 1, 2...`），防止 LLM 幻觉出不存在的 ID
3. **单次 LLM 调用**：将所有新事实和已有记忆一起发给 LLM，返回每个事实的操作决策

| 动作 | 含义 | 执行方式 |
|------|------|----------|
| **ADD** | 全新信息 | 创建新 insight 记忆，生成 embedding |
| **UPDATE** | 更新已有记忆（更详细/更准确） | 归档旧记忆 + 创建新记忆（`ArchiveAndCreate`） |
| **DELETE** | 与已有记忆矛盾 | 软删除：`state → deleted` |
| **NOOP** | 已存在 | 不操作 |

**重要保护**：`pinned` 类型（用户手动保存）的记忆不会被自动 UPDATE 或 DELETE，UPDATE 时改为 ADD。

**源码路径**：`server/internal/service/ingest.go` — `reconcile()` 方法

### 2.4 版本控制与冲突解决

```mermaid
flowchart LR
    A["<b>Agent A</b><br/>更新记忆"] --> CHECK{"If-Match<br/>版本号？"}
    CHECK -- 未提供 --> LWW["<b>直接覆盖 (LWW)</b><br/>version = version + 1"]
    CHECK -- 提供 --> MATCH{"版本匹配？"}
    MATCH -- 匹配 --> WRITE["<b>写入成功</b><br/>version = version + 1"]
    MATCH -- 不匹配 --> WARN["<b>记录警告</b><br/>仍然执行 LWW 覆盖"]
    LWW --> OK(["<b>完成</b>"])
    WRITE --> OK
    WARN --> OK

    style A fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style LWW fill:#fff3e0,stroke:#FF9800,stroke-width:2px
    style OK fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

- **乐观锁**：`UpdateOptimistic` 支持 `expectedVersion` 参数，SQL 中添加 `AND version = ?` 条件
- **LWW（Last Writer Wins）**：版本冲突时记录日志但仍执行覆写，保证写入不会阻塞
- **原子版本递增**：SQL 中 `SET version = version + 1`，无并发竞态

**源码路径**：`server/internal/service/memory.go` — `Update()` 方法，`server/internal/repository/tidb/memory.go` — `UpdateOptimistic()`

---

## 3. 搜索如何工作

### 3.1 搜索策略选择

搜索系统采用**优雅降级**设计，根据可用的基础设施自动选择最佳搜索策略：

```mermaid
flowchart TD
    Q["<b>搜索请求</b><br/>query=关键词"] --> HAS_Q{{"有查询词？"}}
    HAS_Q -- 无 --> LIST["<b>List 模式</b><br/>按 updated_at 排序返回"]
    HAS_Q -- 有 --> AUTO{{"autoModel<br/>已配置？"}}
    AUTO -- 是 --> AUTO_HYBRID["<b>自动混合搜索</b><br/>TiDB EMBED_TEXT<br/>+ FTS/Keyword"]
    AUTO -- 否 --> EMB{{"embedder<br/>已配置？"}}
    EMB -- 是 --> HYBRID["<b>混合搜索</b><br/>客户端 Embedding<br/>+ FTS/Keyword"]
    EMB -- 否 --> FTS{{"FTS 可用？"}}
    FTS -- 是 --> FTS_ONLY["<b>全文搜索</b><br/>FTS_MATCH_WORD (BM25)"]
    FTS -- 否 --> KW["<b>关键词搜索</b><br/>LIKE '%query%'"]

    style Q fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style AUTO_HYBRID fill:#c8e6c9,stroke:#2E7D32,stroke-width:2px
    style HYBRID fill:#c8e6c9,stroke:#2E7D32,stroke-width:2px
    style FTS_ONLY fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style KW fill:#ffebee,stroke:#C62828,stroke-width:2px
    style LIST fill:#f3e5f5,stroke:#7B1FA2,stroke-width:2px
```

**搜索优先级**（从高到低）：

| 优先级 | 策略 | 条件 | 搜索质量 |
|--------|------|------|----------|
| 1 | 自动混合搜索 | `MNEMO_EMBED_AUTO_MODEL` 已配置 | 最高（TiDB 内置 embedding） |
| 2 | 混合搜索 | `MNEMO_EMBED_API_KEY` 已配置 | 高（客户端 embedding） |
| 3 | 全文搜索 | `MNEMO_FTS_ENABLED=true` 且 FTS 可用 | 中（BM25 评分） |
| 4 | 关键词搜索 | 默认 fallback | 低（子串匹配） |

**源码路径**：`server/internal/service/memory.go` — `Search()` 方法

### 3.2 混合搜索算法（RRF）

Mnemos 使用 **RRF（Reciprocal Rank Fusion，倒数排名融合）** 算法合并向量搜索和关键词搜索的结果：

```mermaid
flowchart TD
    QUERY["<b>用户查询</b>"] --> EMBED["<b>生成 Query Embedding</b><br/>embedder.Embed(query)"]
    EMBED --> VEC["<b>向量搜索</b><br/>VEC_COSINE_DISTANCE<br/>取 limit × 3 条"]
    QUERY --> KW["<b>FTS / 关键词搜索</b><br/>FTS_MATCH_WORD 或 LIKE<br/>取 limit × 3 条"]
    VEC --> FILTER["<b>过滤低分结果</b><br/>score < minScore(0.3)<br/>的向量结果被丢弃"]
    FILTER --> RRF["<b>RRF 合并</b><br/>score += 1/(60+rank+1)<br/>向量和关键词各自按 rank 累加"]
    KW --> RRF
    RRF --> DEDUP["<b>去重</b><br/>按 memory ID 合并"]
    DEDUP --> WEIGHT["<b>类型权重</b><br/>pinned 类型 ×1.5"]
    WEIGHT --> SORT["<b>按 RRF 分数降序排列</b>"]
    SORT --> PAGE["<b>分页</b><br/>offset + limit 截取"]
    PAGE --> RESULT(["<b>返回结果</b><br/>附带 score 字段"])

    style QUERY fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
    style RRF fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style RESULT fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

**RRF 公式**：对每个结果的 ID，分别从向量排名和关键词排名中累加分数：

```
score(id) += 1 / (k + rank + 1)    其中 k = 60
```

这种方法的优势：
- 不依赖各搜索分支的原始分数（归一化问题）
- 同时出现在两个结果集中的记忆自然获得更高分
- `pinned` 类型记忆额外获得 1.5x 权重提升

**源码路径**：`server/internal/service/memory.go` — `rrfMerge()`、`hybridSearch()` 方法

### 3.3 向量搜索细节

**三种向量搜索模式**：

| 模式 | SQL 函数 | Embedding 来源 | 使用场景 |
|------|----------|----------------|----------|
| 客户端向量搜索 | `VEC_COSINE_DISTANCE(embedding, ?)` | 服务端调用 OpenAI/Ollama | 自建 embedding 服务 |
| 自动向量搜索 | `VEC_EMBED_COSINE_DISTANCE(embedding, ?)` | TiDB 内置 EMBED_TEXT | TiDB Cloud Serverless |
| 无向量搜索 | — | — | 未配置 embedding |

**关键约束**（TiDB 特有）：
- `VEC_COSINE_DISTANCE` 在 SELECT 和 ORDER BY 中**必须完全一致**，否则无法使用 VECTOR INDEX
- `WHERE embedding IS NOT NULL` 是必须条件
- Score 计算：`score = 1 - distance`（余弦距离转相似度）

**源码路径**：`server/internal/repository/tidb/memory.go` — `VectorSearch()`、`AutoVectorSearch()`

### 3.4 关键词搜索细节

| 搜索类型 | SQL | 评分 | 条件 |
|----------|-----|------|------|
| FTS 全文搜索 | `fts_match_word('query', content)` | BM25 原生评分 | `MNEMO_FTS_ENABLED=true` |
| LIKE 关键词搜索 | `content LIKE CONCAT('%', ?, '%')` | 按 `updated_at` 排序 | 默认 fallback |

FTS 使用 TiDB 的 `MULTILINGUAL` 分词器，天然支持中文。但由于 TiDB 的 FTS 不支持参数化查询，代码通过 `ftsSafeLiteral` 函数进行转义后内联到 SQL 中。

**源码路径**：`server/internal/repository/tidb/memory.go` — `FTSSearch()`、`KeywordSearch()`

---

## 4. OpenClaw 集成使用指南

### 4.1 插件架构

Mnemos 以 `kind: "memory"` 插件形式集成到 OpenClaw，**替代内置的记忆提供者**：

```mermaid
flowchart TD
    subgraph OpenClaw["<b>OpenClaw 框架</b>"]
        direction TB
        FM["<b>框架管理器</b><br/>生命周期调度"]
        PROMPT["<b>Prompt 构建</b>"]
        AGENT["<b>Agent 执行</b>"]
        TOOLS["<b>工具注册</b>"]
    end

    subgraph Plugin["<b>Mnemo Plugin (TypeScript)</b>"]
        direction TB
        IDX["<b>index.ts</b><br/>插件入口 · 注册"]
        BK["<b>server-backend.ts</b><br/>ServerBackend 实现"]
        HK["<b>hooks.ts</b><br/>生命周期钩子"]
        TL["<b>5 个 Memory 工具</b><br/>store · search · get<br/>update · delete"]
    end

    subgraph API["<b>mnemo-server</b>"]
        REST["<b>REST API</b><br/>POST/GET/PUT/DELETE<br/>/v1alpha1/mem9s/{tenantID}/memories"]
    end

    FM --> IDX
    IDX --> TL
    IDX --> HK
    PROMPT --> HK
    AGENT --> TL
    TL --> BK
    HK --> BK
    BK --> REST

    style OpenClaw fill:#e8f4f8,stroke:#2196F3,stroke-width:2px
    style Plugin fill:#fff3e0,stroke:#FF9800,stroke-width:2px
    style API fill:#e8f5e9,stroke:#4CAF50,stroke-width:2px
```

**为什么用 Plugin 而非 Skill？**

| 对比 | Plugin (`kind: "memory"`) | Skill |
|------|--------------------------|-------|
| 触发方式 | 框架自动调用 | Agent 自行决定 |
| 可靠性 | 保证执行 | 依赖 Agent 判断 |
| 集成方式 | 替代内置 memory 工具 | 额外新增工具 |

### 4.2 自动记忆生命周期

```mermaid
sequenceDiagram
    participant User as 用户
    participant OC as OpenClaw 框架
    participant Plugin as Mnemo Plugin
    participant Server as mnemo-server
    participant DB as 数据库

    Note over OC,Plugin: 🔄 每轮对话开始
    OC->>Plugin: before_prompt_build(prompt)
    Plugin->>Server: GET /memories?q={prompt}&limit=10
    Server->>DB: 混合搜索（向量 + 关键词）
    DB-->>Server: 匹配结果
    Server-->>Plugin: 相关记忆列表
    Plugin-->>OC: prependContext(<relevant-memories>)
    Note over OC: Agent 执行时可以看到过往记忆

    Note over OC,Plugin: 💬 对话进行中
    User->>OC: 用户消息
    OC->>OC: Agent 推理与执行
    Note over OC: Agent 可主动调用 memory_store/search 等工具

    Note over OC,Plugin: 🏁 会话结束
    OC->>Plugin: agent_end(messages)
    Plugin->>Plugin: 清洗消息（移除已注入的记忆上下文）
    Plugin->>Plugin: 选择消息（最新 20 条，≤200KB）
    Plugin->>Server: POST /memories {messages: [...]}
    Server->>Server: 智能 Ingest 管道
    Server->>DB: 抽取事实 → 协调 → 写入
    Server-->>Plugin: IngestResult

    Note over OC,Plugin: 🔄 用户执行 /reset
    OC->>Plugin: before_reset(messages)
    Plugin->>Plugin: 生成 session 摘要
    Plugin->>Server: POST /memories {content: summary}
    Server->>DB: 保存摘要
```

**三个生命周期钩子**：

| 钩子 | 触发时机 | 行为 |
|------|----------|------|
| `before_prompt_build` | 每次构建 prompt 前 | 搜索相关记忆 → 注入 prompt 上下文 |
| `agent_end` | 会话结束 | 选择消息 → 调用智能 Ingest 管道 |
| `before_reset` | 用户 `/reset` | 生成 session 摘要 → 存储 |

**记忆格式化**：搜索到的记忆按类型分组展示：
- `[Preferences]` — `pinned` 类型（用户手动保存的偏好）
- `[Knowledge]` — `insight` 类型（自动抽取的知识）

### 4.3 安装与配置

**前提条件**：运行中的 mnemo-server 实例。

**步骤 1：创建租户**

```bash
curl -s -X POST http://<server>/v1alpha1/mem9s | jq .
# → { "id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", "claim_url": "..." }
```

**步骤 2：配置 OpenClaw**

在 `openclaw.json` 中添加：

```json
{
  "plugins": {
    "slots": { "memory": "mnemo" },
    "entries": {
      "mnemo": {
        "enabled": true,
        "config": {
          "apiUrl": "http://localhost:8080",
          "tenantID": "你的租户ID"
        }
      }
    }
  }
}
```

**步骤 3：启动 OpenClaw**

插件自动加载，日志中出现 `[mnemo] Server mode (mnemo-server REST API)` 表示启用成功。

**暴露的 5 个工具**：

| 工具 | 功能 |
|------|------|
| `memory_store` | 存储记忆（content + 可选 tags/metadata） |
| `memory_search` | 混合搜索（q + 可选 tags/source/limit） |
| `memory_get` | 按 ID 获取单条记忆 |
| `memory_update` | 更新已有记忆 |
| `memory_delete` | 删除记忆 |

---

## 5. 本地 MySQL 兼容性分析

```mermaid
graph LR
    subgraph MySQL["<b>本地 MySQL</b>"]
        direction TB
        M_CRUD["✅ <b>CRUD 操作</b><br/>INSERT / UPDATE / DELETE / SELECT"]
        M_KW["✅ <b>关键词搜索</b><br/>LIKE '%query%'"]
        M_VER["✅ <b>版本控制</b><br/>version = version + 1"]
        M_JSON["✅ <b>JSON 列</b><br/>tags / metadata"]
        M_INGEST["✅ <b>智能 Ingest</b><br/>LLM 事实抽取 + 协调"]
        M_MULTI["✅ <b>多租户</b><br/>每租户独立数据库"]
    end

    subgraph TiDB["<b>TiDB Serverless</b>"]
        direction TB
        T_ALL["✅ <b>以上全部功能</b>"]
        T_VEC["✅ <b>向量搜索</b><br/>VEC_COSINE_DISTANCE"]
        T_AUTO["✅ <b>自动 Embedding</b><br/>EMBED_TEXT 内置模型"]
        T_FTS["✅ <b>全文搜索</b><br/>FTS_MATCH_WORD (BM25)"]
        T_VIDX["✅ <b>向量索引</b><br/>ANN 近似最近邻"]
    end

    style MySQL fill:#fff3e0,stroke:#FF9800,stroke-width:2px
    style TiDB fill:#e8f5e9,stroke:#4CAF50,stroke-width:2px
```

### 功能对比详表

| 功能 | TiDB Serverless | 本地 MySQL | 说明 |
|------|:-:|:-:|------|
| 记忆 CRUD | ✅ | ✅ | 标准 SQL，完全兼容 |
| 版本控制（LWW） | ✅ | ✅ | 原子 `version = version + 1` |
| JSON 列（tags/metadata） | ✅ | ✅ | MySQL 5.7+ 支持 `JSON_CONTAINS` |
| 关键词搜索（LIKE） | ✅ | ✅ | 兜底搜索策略，始终可用 |
| 智能 Ingest 管道 | ✅ | ✅ | LLM 调用不依赖数据库类型 |
| 多租户隔离 | ✅ | ✅ | 每租户独立数据库 |
| **向量搜索** | ✅ | ❌ | 需要 `VECTOR` 类型和 `VEC_COSINE_DISTANCE` |
| **自动 Embedding** | ✅ | ❌ | 需要 TiDB 的 `EMBED_TEXT` 函数 |
| **全文搜索（BM25）** | ✅ | ❌ | 需要 TiDB 的 `FTS_MATCH_WORD` |
| **向量索引（ANN）** | ✅ | ❌ | 需要 TiFlash |

### 使用本地 MySQL 的影响

**可以正常使用的功能**：
- 所有记忆 CRUD 操作
- 智能 Ingest 管道（LLM 事实抽取 + 记忆协调）
- 关键词子串搜索（`LIKE '%query%'`）
- 版本控制和冲突解决
- 所有 OpenClaw/Claude Code/OpenCode 插件的生命周期钩子

**不可用的功能**：
- 向量语义搜索 — 只能通过关键词匹配，无法理解语义相似性
- 自动 Embedding — 需要外部 embedding 服务配合，且 MySQL 无法存储向量
- BM25 全文搜索 — 只能退化为 LIKE 子串匹配

**底线结论**：本地 MySQL **可以使用 mnemos 的大部分核心功能**，包括最关键的智能 Ingest 管道（自动记忆管理）。搜索质量会降低（仅关键词匹配），但不影响系统的基本运转。如果搜索质量重要，建议配置外部 Embedding 服务（OpenAI/Ollama）+ 使用 TiDB Serverless 免费版。

### 配置示例（本地 MySQL）

```bash
# 最小配置 — 仅关键词搜索
export MNEMO_DSN="root:password@tcp(127.0.0.1:3306)/mnemos?parseTime=true"

# 推荐配置 — 加上 LLM 实现智能 Ingest
export MNEMO_DSN="root:password@tcp(127.0.0.1:3306)/mnemos?parseTime=true"
export MNEMO_LLM_API_KEY="sk-..."       # 智能事实抽取
export MNEMO_LLM_MODEL="gpt-4o-mini"    # 协调模型

# 注意：以下配置在 MySQL 上不生效
# MNEMO_EMBED_AUTO_MODEL — 需要 TiDB EMBED_TEXT
# MNEMO_FTS_ENABLED — 需要 TiDB FTS_MATCH_WORD
# MNEMO_EMBED_API_KEY — 虽然能生成 embedding，但 MySQL 无法存储 VECTOR 类型
```

> **注意**：`VECTOR(1536)` 类型在建表时可能报错。需要修改 schema，将 `embedding VECTOR(1536) NULL` 替换为一般的列类型或直接移除。建议使用 TiDB Serverless（免费版即可）以获得完整功能。

---

## 6. 数据库 Schema

### 核心表：memories（租户数据面）

```sql
CREATE TABLE IF NOT EXISTS memories (
  id              VARCHAR(36)     PRIMARY KEY,
  content         MEDIUMTEXT      NOT NULL,
  source          VARCHAR(100),
  tags            JSON,
  metadata        JSON,
  embedding       VECTOR(1536)    NULL,          -- TiDB 专属，MySQL 不支持

  memory_type     VARCHAR(20)     NOT NULL DEFAULT 'pinned',  -- pinned | insight | digest
  agent_id        VARCHAR(100)    NULL,
  session_id      VARCHAR(100)    NULL,
  state           VARCHAR(20)     NOT NULL DEFAULT 'active',  -- active | paused | archived | deleted
  version         INT             DEFAULT 1,
  updated_by      VARCHAR(100),
  created_at      TIMESTAMP       DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP       DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  superseded_by   VARCHAR(36)     NULL            -- UPDATE 操作时指向新记忆 ID
);
```

### 控制面表：tenants + tenant_tokens

```sql
-- 租户注册
CREATE TABLE IF NOT EXISTS tenants (
  id              VARCHAR(36)   PRIMARY KEY,
  name            VARCHAR(255)  NOT NULL,
  db_host         VARCHAR(255)  NOT NULL,       -- 租户数据库连接信息
  -- ... 其他连接字段 ...
  status          VARCHAR(20)   NOT NULL DEFAULT 'provisioning',
  schema_version  INT           NOT NULL DEFAULT 1
);

-- 租户 API Token
CREATE TABLE IF NOT EXISTS tenant_tokens (
  api_token       VARCHAR(64)   PRIMARY KEY,
  tenant_id       VARCHAR(36)   NOT NULL
);
```

---

## 7. 核心源码索引

| 模块 | 文件路径 | 核心职责 |
|------|----------|----------|
| **入口与 DI** | `server/cmd/mnemo-server/main.go` | 配置加载、依赖注入、优雅关闭 |
| **配置** | `server/internal/config/config.go` | 环境变量加载（DSN、Embedding、LLM、FTS 等） |
| **核心类型** | `server/internal/domain/types.go` | Memory、MemoryFilter、AuthInfo 类型定义 |
| **错误定义** | `server/internal/domain/errors.go` | ErrNotFound、ErrConflict 等哨兵错误 |
| **HTTP 路由** | `server/internal/handler/handler.go` | chi 路由注册、服务解析缓存 |
| **记忆 Handler** | `server/internal/handler/memory.go` | CRUD HTTP 处理、异步 Ingest |
| **记忆 Service** | `server/internal/service/memory.go` | 混合搜索（RRF）、LWW 更新、BulkCreate |
| **Ingest 管道** | `server/internal/service/ingest.go` | 事实抽取、记忆协调、ADD/UPDATE/DELETE |
| **Repository** | `server/internal/repository/tidb/memory.go` | SQL 实现：VectorSearch、FTSSearch、KeywordSearch |
| **Embedding** | `server/internal/embed/embedder.go` | OpenAI 兼容 API 客户端，nullable 设计 |
| **LLM 客户端** | `server/internal/llm/client.go` | Chat Completions 调用，JSON 模式 |
| **Tenant 中间件** | `server/internal/middleware/auth.go` | URL 租户解析、DB 连接池获取 |
| **限流** | `server/internal/middleware/ratelimit.go` | 按 IP 限流，自动清理过期 visitor |
| **OpenClaw 插件** | `openclaw-plugin/index.ts` | 工具注册、钩子绑定、LazyServerBackend |
| **OpenClaw 后端** | `openclaw-plugin/server-backend.ts` | HTTP → mnemo API 映射 |
| **OpenClaw 钩子** | `openclaw-plugin/hooks.ts` | auto-recall、auto-capture 实现 |
| **OpenCode 插件** | `opencode-plugin/src/index.ts` | 插件入口、配置加载 |
| **Schema** | `server/schema.sql` | 完整 DDL（控制面 + 数据面） |
