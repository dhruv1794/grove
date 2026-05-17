# grove

**A local knowledge forest for AI tools.**

grove indexes your docs, notes, and team exports into a tree-based knowledge
structure that Claude Code, Cursor, Cline, and other AI clients can query
through MCP.

No vector database required. No mandatory cloud indexing. Bring your own model.

> **Status: early development (v0.1 in progress).** The connector and storage
> layers work today; tree building, querying, and the MCP server are landing
> next. See [Roadmap](#roadmap) for what runs now versus what's coming.

---

## Why grove

Knowledge in any real team is scattered across local folders, Obsidian vaults,
Google Drive, Confluence, and Outlook. Searching across all of it is hard;
asking an AI to reason across it is harder.

grove takes a different approach than vector RAG:

- **Tree-of-contents retrieval.** grove builds a navigable tree index over your
  documents and lets an AI descend it the way a person reads a table of
  contents — instead of chunking everything into embeddings.
- **Source-native structure.** Your knowledge already has shape: folder
  hierarchies, Obsidian backlinks, Confluence space trees. grove preserves that
  structure as a *forest* of trees and layers semantic cross-links on top,
  rather than flattening it away.
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

The full v0.1 workflow — once tree building and the MCP server land:

```bash
grove connect obsidian ~/notes
grove build --model ollama/qwen2.5:32b  # build the index with a local model
grove ask "what's our auth flow?" --model anthropic/claude-sonnet-4-6
grove serve --mcp                       # serve the index to AI clients
```

Then, in your AI client: *"Using grove, explain our auth flow and cite the docs."*

## How it works

grove connects to a **source** (a folder, an Obsidian vault, later Drive or
Confluence), normalizes its documents, and builds a **forest** — one
tree-of-contents index per logical grouping in that source. Each tree node
carries an LLM-generated title and summary; semantic cross-links connect related
nodes across trees.

To answer a question, grove selects the trees most likely to contain the answer,
descends them step by step, assembles the relevant leaf documents, and
synthesizes a cited answer. The index is served to AI clients over MCP, so any
MCP-capable tool can query it.

grove is model-agnostic for both build and query — it speaks the
OpenAI-compatible HTTP API, with adapters for Anthropic, Ollama, llama.cpp,
vLLM, Together, and Groq.

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

**Works today:** workspace setup (`init`), the `local` connector with
`.gitignore` and glob filters, PDF and DOCX text extraction, `connect` /
`disconnect` / `sources` / `status`, and a content-addressed SQLite + JSON store.

**v0.1** — tree building (`build`), querying with citations (`ask`), the
`obsidian` connector, incremental `sync`, and an MCP stdio server (`serve --mcp`).

**Later** — Google Drive (v0.2), Confluence (v0.3), Outlook (v0.4), and Slack
(v0.5) connectors; an HTTP transport; and, for the enterprise story,
ACL-aware retrieval (v1.0).

## License

To be finalized before the v0.1.0 release (Apache-2.0 or MIT). grove is and will
remain open source.
