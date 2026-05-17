# grove — handoff package

**Working name:** `grove` (verify availability before committing — common word, may collide on GitHub/brew/npm/domain)

**One-line pitch:** A local knowledge forest for AI tools. grove indexes your docs, notes, and team exports into a tree-based structure that Claude Code, Cursor, Cline, and other AI clients can query through MCP. No vector database required. No mandatory cloud indexing. Bring your own model.

## How to use this package

Open a fresh Claude chat and paste:

> I'm starting development on grove, an open-source AI tooling project. Read these files in order: 00-vision.md, 01-architecture.md, 02-cli-spec.md, 03-data-model.md, 04-connectors.md, 05-mcp-contract.md, 06-build-plan.md, 07-verification-tasks.md. Then summarize what you understand, then ask what to tackle first.

## File map

| # | File | Purpose |
|---|------|---------|
| 00 | 00-vision.md | Why this project, the bet, the core insight |
| 01 | 01-architecture.md | Five-layer design, separation of concerns |
| 02 | 02-cli-spec.md | Exact CLI surface — every command, flag, output |
| 03 | 03-data-model.md | Source, Collection, Tree, Node, Document |
| 04 | 04-connectors.md | Connector interface + per-source notes |
| 05 | 05-mcp-contract.md | MCP server tools, resources, prompts |
| 06 | 06-build-plan.md | Week-by-week v0.1 plan |
| 07 | 07-verification-tasks.md | Prior-art verification (DO THIS FIRST) |
| – | grove-demo.html | Landing page demo |

## Critical context

1. Built by Dhruv Mishra, Samsung Ads. Go preference. Tree/graph index thinker. Bold visual demos.
2. Reference projects: PageIndex (vectorless tree retrieval for long documents) and OpenKB (CLI wiki compiler). grove is **not** a fork of either and **not** a clone of either. The wedge is different — see below.
3. **Revised positioning (post-critique).** The earlier framing — "nobody has done multi-source + tree + BYO-AI + MCP + CLI" — is too strong. PageIndex now exposes MCP/API and a file-system layer. OpenKB is a CLI that compiles documents into an interlinked wiki using PageIndex. Several local-RAG MCP tools index multiple sources into SQLite. The defensible wedge is narrower and clearer:
   > **grove is a local-first, Go-native, connector-first knowledge forest for AI clients: not a wiki generator, not a vector DB, not a hosted SaaS, not tied to one AI client.**
4. **The real thesis.** The user's knowledge graph should be a durable local artifact, not an invisible SaaS index. grove builds that artifact, preserves source-native structure, and serves it to any AI client through standard interfaces.
5. Two target users (v0.1 focus is user 1 only):
   - **(v0.1 target)** Developers and technical leads with important knowledge scattered across local folders, Obsidian vaults, markdown docs, PDFs, and exported team docs.
   - **(v1+ target, aspirational)** Enterprise teams wanting AI search without sending data to a third party. Requires ACL-aware retrieval — explicitly out of scope for v0.1.
6. Strategic order: verify prior art (2h, 07) → de-risk local model retrieval (1 weekend) → ship v0.1 with local + obsidian connectors in 6 weeks → add connectors as releases.
7. Risk: local model retrieval quality is the headline risk. **No quality claims until measured.** The previous package said "88–94% is most likely" — that has been removed. Build the benchmark before claiming a number.

## What v0.1 looks like

```bash
brew install grove
grove init
grove connect local ~/Documents/work
grove connect obsidian ~/notes
grove build --model ollama/qwen2.5:32b
grove ask "what's our auth flow?" --model anthropic/claude-sonnet-4-6
grove serve --mcp
```

Five core verbs. One binary. Works offline (with a local model). Works with any LLM. Open source.

## Public README skeleton (for launch — not this handoff)

```markdown
# grove
A local knowledge forest for AI tools.

grove indexes your local docs, notes, and team exports into a tree-based
knowledge structure that Claude Code, Cursor, Cline, and other AI clients
can query through MCP.

No vector database required. No mandatory cloud indexing. Bring your own model.

## Install
brew install grove

## Quick start
grove init
grove connect local ~/Documents/work
grove build --model ollama/qwen2.5:32b
grove serve --mcp

## In your AI client
"Using grove, explain our auth flow and cite the docs."
```

## Open questions for next session

1. **Final name.** `grove` is good but discoverability may be an issue (common word). Verify availability on GitHub/brew/npm and a domain before committing. Alternatives: forester, treeforge, knit, weave.
2. **Go vs Rust.** Recommendation: Go. Single static binary, mdcompress precedent, MCP Go SDK exists, strong concurrency for parallel connector syncing.
3. **PageIndex prompts.** Recommendation: study PageIndex's public approach, cite clearly, write grove's prompts and implementation from scratch. Avoid forking prompts directly — clean-room inspiration is cleaner on attribution, licensing, and product identity.
4. **Sync semantics.** Recommendation: separate command (`grove sync`) for clarity, even though `grove build` could detect changes. Two verbs, two intents.
5. **Hosted tree registry.** Defer to v1.0+. Not in v0.1.
6. **`grove vs OpenKB` in the README.** Mandatory section. Draft included in 00-vision.md.

## What changed from the previous package

This is a full rewrite incorporating external critique. Major changes:

- **Positioning softened and sharpened.** "Nobody has done it" → "several tools solve parts of this; grove's bet is the local source-native forest abstraction."
- **PageIndex framing changed from defensive to complementary.** PageIndex proves tree retrieval works for long documents; grove extends the pattern to messy multi-source knowledge.
- **OpenKB section added.** Mandatory differentiation table.
- **Quality claims removed.** No "88–94%" until benchmark exists.
- **"Vector RAG is wrong"** softened to "vector-only RAG often fails on professional documents."
- **New first-class concept: `Collection`.** Source ≠ tree. A source contains collections; trees index collections.
- **Node identity vs payload separated.** Stable `node_id` for citations and MCP URIs; `content_hash` for cache and payload addressing.
- **`reasoning_trace` renamed to `retrieval_trace` / `search_path`** in the MCP contract. Avoids exposing chain-of-thought.
- **`--no-reasoning` flag renamed.** It's now `--retrieve-only` (no synthesis) or `--fast` (keyword fallback).
- **"LiteLLM-compatible"** clarified to "OpenAI-compatible HTTP API with provider adapters."
- **v0.1 target user narrowed** to individual devs/PMs. Enterprise pushed to v1+.
- **Non-goals section** added.
- **Permissions warning** added (no ACL mirroring in v0.1).
- **Benchmark design** spelled out — concrete table, not FinanceBench cosplay.
