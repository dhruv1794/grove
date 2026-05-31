package indexer

import (
	"strings"
	"testing"
)

func TestCacheFilename(t *testing.T) {
	const ver, model = "node/v1", "ollama/llama3.1:8b"
	base := cacheFilename(ver, model, "")
	safe := cacheFilename(ver, model, "safe")
	aggr := cacheFilename(ver, model, "aggressive")

	// Every compression level must key to a distinct payload file, so toggling
	// compression regenerates summaries instead of serving stale ones.
	if base == safe || safe == aggr || base == aggr {
		t.Fatalf("compression level did not differentiate the cache filename: base=%q safe=%q aggr=%q", base, safe, aggr)
	}
	// The uncompressed (default) case must keep its historical filename so
	// existing built forests stay cache-valid after this feature lands.
	if strings.Contains(base, "__c-") {
		t.Errorf("uncompressed filename gained a compress suffix: %q", base)
	}
	if !strings.HasSuffix(base, ".json") || !strings.Contains(safe, "__c-safe") {
		t.Errorf("unexpected filenames: base=%q safe=%q", base, safe)
	}
}
