# TiDB Auto Embedding 原理分析与跨数据库复刻方案

> 深入分析 TiDB 的 `EMBED_TEXT()` / `VEC_EMBED_COSINE_DISTANCE()` 内置 embedding 功能，以及在 PostgreSQL 和 SQLite 上的复刻方案。
>
> **注意**：Embedding 与 LLM 是两个独立的能力。Embedding 运行在 mnemo-server 端（用于向量搜索），LLM 运行在 OpenClaw 插件端（用于智能记忆管理）。服务端不需要配置 LLM，只需配置 Embedding。详见 [LLM_ARCHITECTURE.md](./LLM_ARCHITECTURE.md)。

---

## 目录

- [1. TiDB Auto Embedding 是什么](#1-tidb-auto-embedding-是什么)
- [2. 核心函数详解](#2-核心函数详解)
- [3. 内部实现原理](#3-内部实现原理)
- [4. 在 mnemos 中的使用方式](#4-在-mnemos-中的使用方式)
- [5. 跨数据库复刻方案](#5-跨数据库复刻方案)
- [6. 实施计划](#6-实施计划)

---

## 1. TiDB Auto Embedding 是什么

TiDB Auto Embedding 是 TiDB Cloud Starter 集群（AWS 托管）提供的**数据库内置文本向量化**能力。核心理念：**让数据库自己完成 text → vector 的转换**，应用层无需调用任何 embedding API。

```mermaid
graph LR
    subgraph 传统方式["<b>传统方式（客户端 Embedding）</b>"]
        direction TB
        A1["<b>应用代码</b>"] -->|"1. 调用 OpenAI API"| A2["<b>Embedding 服务</b>"]
        A2 -->|"2. 返回向量"| A1
        A1 -->|"3. INSERT 向量"| A3["<b>数据库</b>"]
        A1 -->|"4. 查询时也需先 embed query"| A2
    end

    subgraph TiDB方式["<b>TiDB Auto Embedding</b>"]
        direction TB
        B1["<b>应用代码</b>"] -->|"1. INSERT 纯文本"| B2["<b>TiDB</b>"]
        B2 -->|"2. 内部调用 embedding 模型"| B3["<b>TiDB 内置<br/>Embedding 服务</b>"]
        B3 -->|"3. 向量自动写入<br/>GENERATED 列"| B2
        B1 -->|"4. 查询也只传文本"| B2
    end

    style 传统方式 fill:#ffebee,stroke:#C62828,stroke-width:2px
    style TiDB方式 fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
```

**关键优势**：
- 应用层零 embedding 代码，INSERT 纯文本即可
- 查询时传纯文本，无需先调 embedding API
- 模型内置在 TiDB Cloud 中，部分模型免费（如 Amazon Titan）
- 与 VECTOR INDEX 无缝配合，ANN 搜索自动加速

**限制**：
- 仅在 TiDB Cloud Starter 集群（AWS 托管）上可用
- 不支持自建 TiDB、普通 MySQL、PostgreSQL 或 SQLite
- 模型选择受限于 TiDB Cloud 支持列表

---

## 2. 核心函数详解

### 2.1 EMBED_TEXT()

将文本转为向量 embedding。用于 `GENERATED ALWAYS AS` 子句，**写入时自动触发**。

```sql
-- 建表时定义：content 写入后自动生成 embedding
CREATE TABLE memories (
    id INT PRIMARY KEY AUTO_INCREMENT,
    content TEXT,
    embedding VECTOR(1024) GENERATED ALWAYS AS (
        EMBED_TEXT('tidbcloud_free/amazon/titan-embed-text-v2', content)
    ) STORED
);

-- 插入纯文本，embedding 自动生成
INSERT INTO memories (content) VALUES ('Go 1.22 支持 range over func');
-- → embedding 列自动填充 1024 维向量
```

**工作机制**：
- 类型：`STORED` 生成列（不是 `VIRTUAL`），向量物理存储在磁盘上
- 触发时机：INSERT 和 UPDATE content 列时自动重新计算
- 模型格式：`provider/model_name`（如 `tidbcloud_free/amazon/titan-embed-text-v2`）
- 维度：必须与模型输出维度一致，否则 INSERT 时报错

### 2.2 VEC_EMBED_COSINE_DISTANCE()

**在查询时**自动将文本查询转为向量，然后计算余弦距离。

```sql
-- 传入纯文本查询，TiDB 内部：
-- 1. 调用 EMBED_TEXT 将 "环保能源" 转为向量
-- 2. 计算该向量与 embedding 列的余弦距离
-- 3. 返回距离值用于排序
SELECT id, content
FROM memories
ORDER BY VEC_EMBED_COSINE_DISTANCE(embedding, '环保能源')
LIMIT 5;
```

**内部等价于**：
```sql
-- VEC_EMBED_COSINE_DISTANCE(embedding, '环保能源')
-- ≡
-- VEC_COSINE_DISTANCE(embedding, EMBED_TEXT('model', '环保能源'))
```

也就是说，`VEC_EMBED_COSINE_DISTANCE` 是一个**语法糖**，它知道 `embedding` 列使用的是哪个模型（从 GENERATED 列定义中推断），自动用同一个模型将查询文本转为向量，然后调用普通的 `VEC_COSINE_DISTANCE`。

### 2.3 支持的 Embedding 模型

| 模型 | 提供方 | TiDB 托管（免费） | BYOK（自带 API Key） |
|------|--------|:--:|:--:|
| Amazon Titan | AWS | ✅ | — |
| Cohere | Cohere | ✅ | ✅ |
| Jina AI | Jina | — | ✅ |
| OpenAI | OpenAI | — | ✅ |
| Gemini | Google | — | ✅ |
| Hugging Face | HF Inference | — | ✅ |
| NVIDIA NIM | NVIDIA | — | ✅ |

---

## 3. 内部实现原理

```mermaid
sequenceDiagram
    participant App as 应用
    participant TiDB as TiDB SQL Engine
    participant EmbedSvc as Embedding Service<br/>（TiDB Cloud 内部）
    participant Storage as TiFlash / TiKV

    Note over App,Storage: 📝 写入流程
    App->>TiDB: INSERT INTO memories (content) VALUES ('...')
    TiDB->>TiDB: 检测到 GENERATED 列<br/>需要计算 EMBED_TEXT()
    TiDB->>EmbedSvc: 调用 embedding 模型<br/>（异步批量处理）
    EmbedSvc-->>TiDB: 返回 float[] 向量
    TiDB->>Storage: 写入 content + embedding

    Note over App,Storage: 🔍 查询流程
    App->>TiDB: SELECT ... ORDER BY<br/>VEC_EMBED_COSINE_DISTANCE(embedding, '查询文本')
    TiDB->>TiDB: 解析 embedding 列的模型定义
    TiDB->>EmbedSvc: 将 '查询文本' 转为向量
    EmbedSvc-->>TiDB: 返回 query_vector
    TiDB->>TiDB: 内部重写为<br/>VEC_COSINE_DISTANCE(embedding, query_vector)
    TiDB->>Storage: ANN 向量索引搜索
    Storage-->>TiDB: Top-K 结果
    TiDB-->>App: 返回结果
```

**关键实现细节**：

1. **生成列机制**：`EMBED_TEXT()` 利用 MySQL/TiDB 的 `GENERATED ALWAYS AS ... STORED` 机制。`STORED` 意味着向量在写入时计算并物理存储，查询时不需要重新计算。

2. **模型绑定**：每个 GENERATED 列绑定一个固定的 embedding 模型。`VEC_EMBED_COSINE_DISTANCE` 在执行时从列的生成表达式中提取模型名称，用相同模型处理查询文本。

3. **异步并发**：TiDB 内部对 embedding 调用做了异步批量优化。大批量 INSERT 时不会串行调用 embedding API。

4. **向量索引兼容**：GENERATED 列上可以创建 `VECTOR INDEX`，与手动写入的向量列完全等价。定义索引时用 `VEC_COSINE_DISTANCE`，查询时用 `VEC_EMBED_COSINE_DISTANCE`。

---

## 4. 在 mnemos 中的使用方式

mnemos 通过 `MNEMO_EMBED_AUTO_MODEL` 环境变量启用 Auto Embedding：

```bash
export MNEMO_EMBED_AUTO_MODEL="tidbcloud_free/amazon/titan-embed-text-v2"
export MNEMO_EMBED_AUTO_DIMS=1024
```

启用后的行为变化：

| 操作 | 未启用（客户端 embedding） | 启用 Auto Embedding |
|------|--------------------------|---------------------|
| **建表** | `embedding VECTOR(1536) NULL` | `embedding VECTOR(1024) GENERATED ALWAYS AS (EMBED_TEXT(...)) STORED` |
| **写入** | 服务端调 OpenAI/Ollama 生成向量后 INSERT | 直接 INSERT content，TiDB 自动生成向量 |
| **搜索** | 先 embed query，再 `VEC_COSINE_DISTANCE(embedding, ?)` | 直接 `VEC_EMBED_COSINE_DISTANCE(embedding, ?)` 传文本 |
| **更新** | content 变更时重新调 embedding API | TiDB 自动重新生成 |

源码中的关键判断逻辑（`server/internal/service/memory.go`）：
```go
// 搜索路由选择（服务端 Embedding，独立于 LLM）
if s.autoModel != "" {
    return s.autoHybridSearch(ctx, filter)  // → TiDB AutoVectorSearch
}
if s.embedder != nil {
    return s.hybridSearch(ctx, filter)       // → 客户端 VectorSearch
}
// fallback: FTS or keyword（无 Embedding 配置时）
```

```go
// 写入时生成 embedding（与 LLM 无关，纯 Embedding API）
if s.autoModel == "" && s.embedder != nil {
    embedding, _ = s.embedder.Embed(ctx, content)  // 调用 MNEMO_EMBED_* 配置的 API
}
// autoModel != "" 时不生成 embedding，由 TiDB GENERATED 列自动处理
```

> 这些 Embedding 调用完全不涉及 LLM。服务端使用 `MNEMO_EMBED_*` 环境变量配置 Embedding 提供商（如 Ollama `bge-m3`），与插件侧的 `llmApiKey` 配置互相独立。

---

## 5. 跨数据库复刻方案

### 5.1 方案总览

```mermaid
graph TD
    subgraph 方案A["<b>方案 A：服务端 Embedding 代理</b><br/>（推荐）"]
        direction TB
        A1["<b>mnemo-server</b><br/>在写入和查询时<br/>统一调用 embedding API"]
        A2["<b>OpenAI / Ollama</b><br/>外部 embedding 服务"]
        A3["<b>PG / SQLite / MySQL</b><br/>存储预计算向量"]
        A1 --> A2
        A1 --> A3
    end

    subgraph 方案B["<b>方案 B：PG 触发器 + plpython</b><br/>（仅 PG）"]
        direction TB
        B1["<b>INSERT 触发器</b><br/>拦截写入"]
        B2["<b>plpython3u</b><br/>调用 OpenAI API"]
        B3["<b>pgvector 列</b><br/>存储向量"]
        B1 --> B2
        B2 --> B3
    end

    subgraph 方案C["<b>方案 C：PG pg_vectorize 扩展</b><br/>（仅 PG）"]
        direction TB
        C1["<b>pg_vectorize</b><br/>开源扩展"]
        C2["<b>自动 embedding</b><br/>类似 TiDB EMBED_TEXT"]
        C1 --> C2
    end

    style 方案A fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style 方案B fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style 方案C fill:#e3f2fd,stroke:#1565C0,stroke-width:2px
```

### 5.2 方案 A：服务端 Embedding 代理（推荐）

**原理**：在 mnemo-server 的 Service 层统一拦截写入和查询，调用外部 embedding API。这正是 mnemos 当前 `embedder != nil` 分支已经实现的逻辑。

**与 TiDB Auto Embedding 的等价关系**：

| TiDB Auto Embedding | 服务端代理（当前实现） |
|---------------------|----------------------|
| `EMBED_TEXT()` GENERATED 列 | `embedder.Embed(content)` 在 Create/Update 时调用 |
| `VEC_EMBED_COSINE_DISTANCE(col, text)` | `embedder.Embed(query)` → `VEC_COSINE_DISTANCE(col, vec)` |
| 数据库内部调用 | 服务端调用，结果存入数据库 |

**实施方式**（已基本实现）：

```go
// 写入时（ingest.go / memory.go）
if s.autoModel == "" && s.embedder != nil {
    embedding, _ = s.embedder.Embed(ctx, content)
}

// 搜索时（memory.go）
if s.embedder != nil {
    queryVec, _ := s.embedder.Embed(ctx, filter.Query)
    return s.memories.VectorSearch(ctx, queryVec, filter, limit)
}
```

**增强方案**：为 PG/SQLite 实现 `AutoVectorSearch`，让它在服务端完成 embedding 转换：

```go
// postgres/memory.go — 增强版 AutoVectorSearch
func (r *MemoryRepo) AutoVectorSearch(ctx context.Context, queryText string, f domain.MemoryFilter, limit int) ([]domain.Memory, error) {
    // 当前返回 nil, nil
    // 增强：接受一个 Embedder 参数，在此处调用
    // 但这需要改接口……更好的做法是在 Service 层处理
    return nil, nil
}
```

**结论**：方案 A 不需要额外改动 repository 层。核心逻辑已在 Service 层完成。PG 和 SQLite 的 `AutoVectorSearch` 返回 `nil, nil` 是正确的，因为 Service 层的搜索路由会在 `autoModel == ""` 时走 `hybridSearch` 分支（使用客户端 embedder）。

**唯一需要确保的是**：使用 PG/SQLite 时，配置 `MNEMO_EMBED_API_KEY`（客户端 embedding），而不是 `MNEMO_EMBED_AUTO_MODEL`（TiDB 专属）。

### 5.3 方案 B：PostgreSQL 触发器 + plpython3u

**原理**：在 PG 中通过触发器和 plpython3u 扩展，实现类似 TiDB GENERATED 列的自动 embedding。

```sql
-- 1. 启用扩展
CREATE EXTENSION IF NOT EXISTS plpython3u;
CREATE EXTENSION IF NOT EXISTS vector;

-- 2. 创建 embedding 函数
CREATE OR REPLACE FUNCTION generate_embedding(content TEXT)
RETURNS vector(1536)
LANGUAGE plpython3u
AS $$
import json, urllib.request
req = urllib.request.Request(
    'https://api.openai.com/v1/embeddings',
    data=json.dumps({
        "input": content,
        "model": "text-embedding-3-small",
        "encoding_format": "float"
    }).encode(),
    headers={
        'Authorization': f'Bearer {plpy.execute("SELECT current_setting(\'app.openai_key\')")[0]["current_setting"]}',
        'Content-Type': 'application/json'
    }
)
resp = json.loads(urllib.request.urlopen(req).read())
vec = resp['data'][0]['embedding']
return str(vec)
$$;

-- 3. 创建触发器
CREATE OR REPLACE FUNCTION auto_embed_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.content IS NOT NULL AND (TG_OP = 'INSERT' OR NEW.content != OLD.content) THEN
        NEW.embedding := generate_embedding(NEW.content);
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_auto_embed
BEFORE INSERT OR UPDATE ON memories
FOR EACH ROW EXECUTE FUNCTION auto_embed_trigger();
```

**优缺点**：

| 优点 | 缺点 |
|------|------|
| 完全在数据库层面，应用无感 | 需要 `plpython3u`（超级用户权限） |
| 与 TiDB EMBED_TEXT 行为最接近 | 同步调用 API，INSERT 变慢 |
| embedding 和数据在同一事务中 | 难以做批量优化 |
| — | API 失败会导致 INSERT 失败 |

**评估**：适合 POC 验证，不推荐生产使用。同步 API 调用会严重拖慢写入性能。

### 5.4 方案 C：pg_vectorize 扩展

[pg_vectorize](https://github.com/tembo-io/pg_vectorize) 是一个开源 PG 扩展，提供类似 TiDB Auto Embedding 的能力：

```sql
-- 安装后使用
SELECT vectorize.table(
    job_name => 'memories_embed',
    "table" => 'memories',
    primary_key => 'id',
    columns => ARRAY['content'],
    transformer => 'openai/text-embedding-3-small',
    schedule => 'realtime'
);
```

它会自动：
- 创建一个 embedding 列
- 监听 INSERT/UPDATE，异步生成 embedding
- 提供搜索函数

**优缺点**：

| 优点 | 缺点 |
|------|------|
| 最接近 TiDB Auto Embedding 体验 | 需要安装 Rust 编译的扩展 |
| 异步处理，不阻塞写入 | 依赖外部扩展维护 |
| 支持多种 embedding 提供商 | 仅限 PostgreSQL |
| 内置调度和重试 | 云托管 PG 可能不支持安装 |

**评估**：如果使用自建 PostgreSQL 且可以安装扩展，这是最优雅的方案。

### 5.5 方案对比

```mermaid
graph TD
    START{{"使用什么数据库？"}} --> TIDB["<b>TiDB Cloud Starter</b>"]
    START --> PG["<b>PostgreSQL</b>"]
    START --> SQLITE["<b>SQLite</b>"]
    START --> MYSQL["<b>普通 MySQL</b>"]

    TIDB --> TIDB_AUTO["✅ <b>EMBED_TEXT + VEC_EMBED_COSINE_DISTANCE</b><br/>零代码，数据库内置"]

    PG --> PG_Q{{"可以安装扩展？"}}
    PG_Q -- 是 --> PG_EXT["✅ <b>方案 C: pg_vectorize</b><br/>最接近 TiDB 体验"]
    PG_Q -- 否 --> PG_SRV["✅ <b>方案 A: 服务端代理</b><br/>配置 MNEMO_EMBED_API_KEY"]

    SQLITE --> SQLITE_SRV["✅ <b>方案 A: 服务端代理</b><br/>配置 MNEMO_EMBED_API_KEY"]
    MYSQL --> MYSQL_SRV["✅ <b>方案 A: 服务端代理</b><br/>配置 MNEMO_EMBED_API_KEY"]

    style TIDB_AUTO fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style PG_EXT fill:#e8f5e9,stroke:#2E7D32,stroke-width:2px
    style PG_SRV fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style SQLITE_SRV fill:#fff8e1,stroke:#F9A825,stroke-width:2px
    style MYSQL_SRV fill:#fff8e1,stroke:#F9A825,stroke-width:2px
```

| 方案 | 适用数据库 | 复杂度 | 与 TiDB 等价度 | 生产可用 |
|------|----------|--------|:--:|:--:|
| **TiDB Auto Embedding** | TiDB Cloud | 零 | 100% | ✅ |
| **方案 A: 服务端代理** | 全部 | 低（已实现） | ~80% | ✅ |
| **方案 B: PG 触发器** | PostgreSQL | 中 | ~90% | ⚠️ |
| **方案 C: pg_vectorize** | PostgreSQL | 中 | ~95% | ✅ |

---

## 6. 实施计划

### 对于 mnemos 当前代码

**Embedding 与 LLM 完全解耦**。服务端只负责 Embedding（向量搜索），不处理 LLM（智能管线）：

- `MNEMO_EMBED_AUTO_MODEL` 非空 → TiDB 模式，使用 `EMBED_TEXT` + `VEC_EMBED_COSINE_DISTANCE`
- `MNEMO_EMBED_API_KEY` 非空 → 客户端模式，服务端调 OpenAI/Ollama（方案 A）
- 两者都为空 → 仅关键词搜索
- ~~`MNEMO_LLM_*`~~ → **已移除**。LLM 调用由 OpenClaw 插件处理。

PG 和 SQLite 用户只需配置 `MNEMO_EMBED_API_KEY`（以及可选的 `MNEMO_EMBED_BASE_URL` 指向 Ollama），即可获得等价的向量搜索能力。智能记忆管理（事实提取 + 协调）通过插件侧 LLM（如百炼 Coding Plan）实现。

### 配置示例

```bash
# 方案 1: PostgreSQL + 阿里云百炼（中文最优，有免费额度）
MNEMO_DB_DRIVER=postgres
MNEMO_DSN="postgres://user:pass@localhost:5432/mnemos?sslmode=disable"
MNEMO_EMBED_API_KEY="sk-xxxxxx"                                        # 百炼普通 API Key（非 Coding Plan）
MNEMO_EMBED_BASE_URL="https://dashscope.aliyuncs.com/compatible-mode/v1"
MNEMO_EMBED_MODEL="text-embedding-v4"
MNEMO_EMBED_DIMS=1024

# 方案 2: PostgreSQL + Ollama 本地 embedding（完全免费）
MNEMO_DB_DRIVER=postgres
MNEMO_DSN="postgres://user:pass@localhost:5432/mnemos?sslmode=disable"
MNEMO_EMBED_API_KEY="local"
MNEMO_EMBED_BASE_URL="http://localhost:11434/v1"
MNEMO_EMBED_MODEL="bge-m3"                                             # 中文推荐 bge-m3
MNEMO_EMBED_DIMS=1024

# 方案 3: SQLite + OpenAI
MNEMO_DB_DRIVER=sqlite
MNEMO_DSN="/data/mnemos.db"
MNEMO_EMBED_API_KEY="sk-proj-..."
MNEMO_EMBED_MODEL="text-embedding-3-small"
MNEMO_EMBED_DIMS=1536
```

> **注意**：百炼 Coding Plan（`sk-sp-` 开头的 Key）**不包含** Embedding 模型且禁止后端 API 调用，不能用于服务端 Embedding。需使用百炼按量付费的普通 API Key 或本地 Ollama。
>
> **Coding Plan 的正确用法**：仅用于 OpenClaw 插件的 `llmApiKey` 配置（LLM 调用），不用于服务端的 `MNEMO_EMBED_*` 配置（Embedding 调用）。详见 [PROVIDER_GUIDE.md](./PROVIDER_GUIDE.md) 和 [LLM_ARCHITECTURE.md](./LLM_ARCHITECTURE.md)。
