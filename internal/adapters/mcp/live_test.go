package mcp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestLive_MCP drives the real grove binary as an MCP server over stdio against
// a built+embedded workspace — the end-to-end DOD. Gated: set GROVE_MCP_BIN
// (path to a built grove) and GROVE_MCP_WS (a built workspace). Set
// GROVE_MCP_MODEL (e.g. ollama/qwen2.5:7b) to also exercise grove_search.
//
//	GROVE_MCP_BIN=./bin/grove GROVE_MCP_WS=~/grove-bench/ws \
//	GROVE_MCP_MODEL=ollama/qwen2.5:7b go test ./internal/adapters/mcp -run Live -v
func TestLive_MCP(t *testing.T) {
	bin := os.Getenv("GROVE_MCP_BIN")
	ws := os.Getenv("GROVE_MCP_WS")
	if bin == "" || ws == "" {
		t.Skip("set GROVE_MCP_BIN and GROVE_MCP_WS to run the live MCP smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	model := os.Getenv("GROVE_MCP_MODEL")
	cmd := exec.Command(bin, "serve", "--mcp")
	cmd.Env = append(os.Environ(), "GROVE_WORKSPACE="+ws)
	if model != "" {
		cmd.Env = append(cmd.Env, "GROVE_QUERY_MODEL="+model)
	}
	cmd.Stderr = os.Stderr // grove diagnostics; the protocol rides stdin/stdout

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "grove-live-test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to grove serve --mcp: %v", err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	t.Logf("tools: %d", len(tools.Tools))

	// list_trees → grab a tree id → navigate it → read a leaf.
	var lt listTreesOut
	callJSON(t, cs, "grove_list_trees", nil, &lt)
	if len(lt.Trees) == 0 {
		t.Fatal("no trees in the live forest")
	}
	tree := lt.Trees[0]
	t.Logf("tree %q: %d docs / %d nodes — %s", tree.Name, tree.DocCount, tree.NodeCount, firstLine(tree.Summary))

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "grove_navigate", Arguments: map[string]any{"tree_id": tree.ID},
	})
	if err != nil || res.IsError {
		t.Fatalf("navigate root: err=%v isErr=%v %s", err, res.IsError, toolText(res))
	}
	t.Logf("navigate root: %s", toolText(res))

	// Read the forest resource.
	rr, err := cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "grove://forest"})
	if err != nil || len(rr.Contents) == 0 {
		t.Fatalf("read grove://forest: %v", err)
	}

	// Pattern B search — only with a model (passed to the server via cmd.Env).
	if model != "" {
		sres, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
			Name: "grove_search", Arguments: map[string]any{"query": "how does horizontal pod autoscaling work?"},
		})
		if err != nil {
			t.Fatalf("grove_search: %v", err)
		}
		t.Logf("grove_search: isErr=%v %s", sres.IsError, toolText(sres))
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
