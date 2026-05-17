# Troubleshooting

**No documents indexed.** Check that the source path exists and is not
fully covered by a `.gitignore` rule.

**Slow first build.** The node cache is empty on the first run; later
builds reuse cached nodes.
