# grove

**A local knowledge forest for AI tools.**

grove indexes your docs, notes, and team exports into a tree-based knowledge
structure that Claude Code, Cursor, Cline, and other AI clients can query
through MCP.

Semantic search built in, no vector database to run. No mandatory cloud indexing. Bring your own model.

> **Status: v0.1 in progress.** Connecting sources, building the forest,
> embeddings, and the full query ensemble (`build`, `embed`, `ask`, `sync`,
> `repl`) all work today. The **MCP server (`serve --mcp`) is the last v0.1
> milestone** and is not built yet. See [Roadmap](#roadmap).

---

## Why grove

Knowledge in any real team is scattered across local folders, Obsidian vaults,
Google Drive, Confluence, and Outlook. Searching across all of it is hard;
asking an AI to reason across it is harder.

grove takes a different approach than single-retriever vector RAG:

- **Ensemble retrieval.** No single retrieval method wins on its own. grove
  fuses three — keyword (full-text), semantic (embeddings), and tree-of-contents
  navigation — by reciprocal rank fusion, so each covers the others' blind
  spots. Optional LLM stages (sub-query decomposition, graded reranking) sharpen
  results further when you bring a capable model.
- **Source-native structure.** Your knowledge already has shape: folder
  hierarchies, Obsidian backlinks, Confluence space trees. grove preserves that
  structure as a *forest* of trees and layers semantic cross-links on top,
  rather than flattening it away. The tree is both browsable and one of the
  retrievers.
- **No vector database to run.** Embeddings live as plain blobs in the same
  SQLite file and are scored by brute-force cosine — semantic search with no
  separate vector service to operate (fine to ~250k docs).
- **Build once, query with anything.** Indexing and reasoning are separate model
  choices. Build the index with a free local model; query it with whatever fits
  the task — Claude for quality, a local model for privacy.
- **A durable local artifact.** The index lives on your disk as SQLite plus
  plain JSON. It's yours, it's diffable, and it's shareable — not an invisible
  SaaS index.

## Install

grove is a single static Go binary. While it's pre-release, build from source:

```bash
git clone https://github.com/dhruvmishra/grove
cd grove
make build          # → ./bin/grove
```

Requires Go 1.25+. A Homebrew tap (`brew install grove`) ships with v0.1.0.

## Quick start

```bash
grove init                              # create a workspace
grove connect local ~/Documents/work    # connect a folder of docs
grove status                            # see what's connected
```

The full workflow (everything except `serve` works today):

```bash
grove connect obsidian ~/notes
grove build --model ollama/qwen2.5:32b  # build the index with a local model
grove embed                             # add semantic embeddings (bge-m3 by default)
grove ask "what's our auth flow?" --model anthropic/claude-sonnet-4-6
grove repl                              # interactive question session (TUI)
grove serve --mcp                       # serve the index to AI clients (coming in v0.1)
```

Then, in your AI client: *"Using grove, explain our auth flow and cite the docs."*

## How it works

grove connects to a **source** (a folder, an Obsidian vault, later Drive or
Confluence), normalizes its documents, and builds a **forest** — one
tree-of-contents index per logical grouping in that source. Each tree node
carries an LLM-generated title and summary; semantic cross-links connect related
nodes across trees.

To answer a question, grove runs several retrievers — keyword search, semantic
embeddings, and step-by-step tree descent — and fuses their results by
reciprocal rank fusion, then synthesizes a cited answer. A `--mode` flag picks
how much work to do: `fast` (keyword + embeddings, no model calls, instant),
`balanced` (adds sub-query decomposition; the default), `quality` (adds a graded
rerank; best results, wants a capable model), and `deep` (adds explainable tree
navigation). The index is served to AI clients over MCP, so any MCP-capable tool
can query it.

grove is model-agnostic for both build and query — it speaks the
OpenAI-compatible HTTP API, with adapters for Anthropic, Ollama, llama.cpp,
vLLM, Together, Groq, and DeepSeek.

## Benchmark

Measured on a hard, multi-doc question set (60 questions over 1,668 Kubernetes
docs), with `bge-m3` embeddings. The headline metric is **docRecall@12** — the
fraction of a question's expected documents found in the top 12 — because
ground truth is multi-doc and "found one of four" isn't success. All rows use
the *same* embedder, so the comparison is apples-to-apples.

| Pipeline | docRecall@12 | hit@12 | MRR | latency | needs a model? |
|---|---|---|---|---|---|
| **grove fast** (keyword + embeddings) | 0.72 | 97% | 0.749 | ~0.1s | no |
| **grove balanced** (+ decompose) | **0.85** | 98% | 0.823 | ~1.5s | any (8b ok) |
| **grove quality** (+ graded rerank) | 0.86 | 98% | 0.743 | ~2.4s | strong only |
| vanilla vector RAG (embeddings only) | 0.55 | 88% | 0.547 | ~0.1s | no |
| advanced RAG (embeddings + rerank) | 0.62 | 90% | 0.669 | ~2.4s | strong only |

Two honest takeaways: grove's *free, model-free* fast tier already beats
vanilla vector RAG (0.72 vs 0.55), and the default balanced tier reaches **~1.5×**
the recall of vanilla RAG — because coverage (keyword + tree + sub-query
decomposition) wins, not reranking. The LLM-stage rows here used DeepSeek; the
model-free rows are a clean embedder comparison. Full methodology, model-class
caveats, and the runs that came out *negative* are in
[`documents/benchmark-findings.md`](documents/benchmark-findings.md).

## How grove compares

Several tools solve parts of this problem. grove's bet is the local,
source-native forest abstraction.

| Project | What it is | How grove differs |
|---|---|---|
| **PageIndex** | Tree retrieval for single long documents; MCP/API | grove is multi-source connector infrastructure with a source-native forest, not a single-document tree. Complementary. |
| **qmd** | Local CLI search over a single collection of files (BM25 + vectors) | grove has source connectors and a forest abstraction across heterogeneous sources. |
| **OpenKB** | CLI that compiles documents into a human-readable wiki | grove serves an index to AI clients over MCP; it does not generate a wiki to read. |
| **LLM Wiki implementations** | Generate a browsable wiki, often from CLI session transcripts | grove serves an index, and targets work documents rather than session logs. |
| **Glean / Dust / NotebookLM** | Hosted, managed knowledge AI | grove is local-first, open source, bring-your-own-model, with no SaaS lock-in. |
| **Onyx** | Open-source unified search *server* with connectors | grove is a single-binary CLI, not server software — a different deployment story. |

## Non-goals

Constraints, stated up front:

- grove is **not an autonomous agent** — it is a retrieval system.
- grove does **not edit** your source documents.
- grove does **not require a vector database**.
- grove does **not send documents to a cloud model** unless you explicitly pick
  a cloud model with `--model`.
- grove is **not a generated wiki**. The output is a serving index, not a
  human reading artifact.
- grove is **not a SaaS**. There is no hosted version.

## Permissions notice

> v0.1 assumes all connected sources are readable by the local user. Do not use
> grove as a shared team server until ACL-aware retrieval lands. Cloud
> connectors (Drive, Confluence, Outlook) in later versions will pull only what
> the authenticated user can read; grove does not currently mirror or enforce
> per-document ACLs at query time.

## Roadmap

**Works today:** workspace setup (`init`); the `local` and `obsidian`
connectors with `.gitignore`/glob filters, PDF + DOCX text extraction, and
wikilink/backlink cross-links; the content-addressed SQLite + JSON store;
forest building (`build`) with topic grouping and 100%-cache incremental
rebuilds; semantic embeddings (`embed`); the full query ensemble (`ask`) with
`--mode fast/balanced/quality/deep`, decompose/prune/rerank stages, and CRAG
abstention (`--correct`); incremental `sync --watch`; an interactive TUI
(`repl`); and layered local/global config (`config`).

**Remaining for v0.1** — the MCP stdio server (`serve --mcp`): the headline
"plug grove into any AI client" surface. This is the next milestone.

**Later** — Google Drive (v0.2), Confluence (v0.3), Outlook (v0.4), and Slack
(v0.5) connectors; an HTTP transport; and, for the enterprise story,
ACL-aware retrieval (v1.0).

## License

To be finalized before the v0.1.0 release (Apache-2.0 or MIT). grove is and will
remain open source.
