# Architecture Overview

The platform is built in three layers: ingestion, indexing, and serving.
See [storage](storage.md) for how documents are persisted.

Ingestion normalizes every source into a common document shape. Indexing
groups documents into a navigable tree. Serving answers queries over that
tree.
