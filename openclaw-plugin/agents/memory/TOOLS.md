# Tools

You operate through four tools. Every action MUST be a tool call — never output raw text as your final answer.

## vmem_search

Search existing memories. **Always call this before storing** to avoid duplicates.

- `q` (string, required): Natural language search query
- `tags` (string): Comma-separated tags for filtering (e.g. "tech-stack,language")
- `limit` (number): Max results (default 20)

**Tips:**
- Use broad queries first, then narrow down
- Search returns `score` (0–1); anything above 0.6 is a strong match
- Results include `id`, `content`, `tags`, `memory_type`, `score`

## vmem_store

Store a new fact as a memory.

- `content` (string, required): The fact to store. One idea per memory.
- `tags` (array of strings, required): 2–5 tags. Lowercase, hyphen-separated. Let the content guide your tag choices — be natural and descriptive. Reuse tags you see in search results for consistency.
- `source` (string): Source identifier (auto-set to "vmem-auto")
- `metadata` (object): Arbitrary structured data

**Tips:**
- Be specific: "Uses Go 1.22 with chi router for REST APIs" > "Uses Go"
- Preserve the user's original language
- One atomic fact per memory

## vmem_update

Update an existing memory by its UUID.

- `id` (string, required): Memory UUID from search results
- `content` (string): New content (replaces old)
- `tags` (array of strings): Replacement tags. Reuse existing tags from search results where appropriate.

**Tips:**
- Use this when a fact has evolved, not when it's contradicted (use delete + store instead)
- Always provide updated tags when changing content

## vmem_delete

Delete an obsolete or contradicted memory.

- `id` (string, required): Memory UUID from search results

**Tips:**
- Only delete when the memory is objectively wrong or superseded
- Never delete `memory_type: "pinned"` memories
