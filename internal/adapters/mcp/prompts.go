package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"grove/internal/core"
)

// Prompts are canned starting messages that steer a client toward grove's
// tools. They contain no model output — just templated user turns (05 §Prompts).
func (s *Server) registerPrompts() {
	s.srv.AddPrompt(&mcpsdk.Prompt{
		Name:        "grove_brief",
		Title:       "Brief me on my forest",
		Description: "Summarize what's in the grove forest.",
	}, promptBrief)

	s.srv.AddPrompt(&mcpsdk.Prompt{
		Name:        "grove_compare_sources",
		Title:       "Compare two sources",
		Description: "Compare what two sources say about a topic.",
		Arguments: []*mcpsdk.PromptArgument{
			{Name: "source_a", Description: "first source name", Required: true},
			{Name: "source_b", Description: "second source name", Required: true},
			{Name: "topic", Description: "the topic to compare", Required: true},
		},
	}, promptCompareSources)

	s.srv.AddPrompt(&mcpsdk.Prompt{
		Name:        "grove_what_changed",
		Title:       "What changed recently",
		Description: "Summarize what changed in the forest over a recent window.",
		Arguments: []*mcpsdk.PromptArgument{
			{Name: "duration", Description: "e.g. \"7 days\", \"last week\"", Required: true},
		},
	}, promptWhatChanged)
}

func userPrompt(text string) *mcpsdk.GetPromptResult {
	return &mcpsdk.GetPromptResult{
		Messages: []*mcpsdk.PromptMessage{{
			Role:    "user",
			Content: &mcpsdk.TextContent{Text: text},
		}},
	}
}

func promptBrief(_ context.Context, _ *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	r := userPrompt("Brief me on what's in my grove forest. Start with grove_list_trees " +
		"to see the trees and their summaries, then describe the main topics and which " +
		"sources they come from. Use grove_search if you need detail on a specific area.")
	r.Description = "Summarize what's in the grove forest."
	return r, nil
}

func promptCompareSources(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	a := req.Params.Arguments["source_a"]
	b := req.Params.Arguments["source_b"]
	topic := req.Params.Arguments["topic"]
	if a == "" || b == "" || topic == "" {
		return nil, core.NewError(core.KindMisuse,
			"grove_compare_sources needs source_a, source_b, and topic",
			"provide all three arguments")
	}
	return userPrompt(fmt.Sprintf(
		"Compare what the %q and %q sources say about %q. Run grove_search twice "+
			"(once scoped to each source via the source argument), then contrast the two "+
			"answers, citing the documents each relied on.", a, b, topic)), nil
}

func promptWhatChanged(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	dur := req.Params.Arguments["duration"]
	if dur == "" {
		return nil, core.NewError(core.KindMisuse,
			"grove_what_changed needs a duration",
			"provide a duration like \"7 days\"")
	}
	return userPrompt(fmt.Sprintf(
		"Summarize what changed in my grove forest in the last %s. Use grove_list_trees "+
			"to orient, then grove_search to surface the documents and topics that appear "+
			"to be new or recently updated.", dur)), nil
}
