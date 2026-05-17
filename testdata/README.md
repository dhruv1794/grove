# testdata

A small fixture corpus for grove's end-to-end tests, demos, and the
Week-6 benchmark. **Not** used by unit tests — connector tests generate
their own fixtures inline (see `makeTestPDF` / `makeTestDOCX`).

## Layout

- `corpus/` — a plain Markdown knowledge base with nested folders and
  relative Markdown links.
- `obsidian/` — an Obsidian-shaped subset: an `.obsidian/` vault marker,
  YAML frontmatter, `[[wikilinks]]` (with `#heading` anchors and
  `|aliases`), and inline `#tags`.
- `files/report.pdf`, `files/memo.docx` — binary document formats, to
  exercise PDF and DOCX text extraction.

## Regenerating the binaries

The PDF and DOCX are generated from plain text with macOS tools, so they
stay reproducible. Recreate them from the repo root:

```sh
mkdir -p testdata/files

cat > /tmp/report.txt <<'EOF'
Quarterly Platform Report

Ingestion handled 1,240 documents this quarter across four sources.
Indexing produced 312 tree nodes. The node cache hit rate on incremental
builds averaged 94 percent.

Next quarter: add the Obsidian connector and ship the MCP server.
EOF
cupsfilter /tmp/report.txt > testdata/files/report.pdf

cat > /tmp/memo.txt <<'EOF'
Memo: Deprecating the legacy exporter

The legacy exporter is replaced by the local connector. Teams should
reconnect their sources before the end of the month.

Questions go to the platform team.
EOF
textutil -convert docx /tmp/memo.txt -output testdata/files/memo.docx
```
