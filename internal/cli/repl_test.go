package cli

import (
	"strings"
	"testing"
)

func TestHandleReplCommand(t *testing.T) {
	sources := []string{"k8s", "notes"}

	t.Run("quit", func(t *testing.T) {
		st := &replState{mode: "balanced"}
		if _, quit := handleReplCommand(st, sources, ":quit"); !quit {
			t.Error(":quit should end the session")
		}
	})

	t.Run("set mode", func(t *testing.T) {
		st := &replState{mode: "balanced"}
		handleReplCommand(st, sources, ":mode quality")
		if st.mode != "quality" {
			t.Errorf("mode = %q, want quality", st.mode)
		}
	})

	t.Run("reject bad mode", func(t *testing.T) {
		st := &replState{mode: "balanced"}
		reply, _ := handleReplCommand(st, sources, ":mode turbo")
		if st.mode != "balanced" {
			t.Errorf("bad mode changed state to %q", st.mode)
		}
		if !strings.Contains(reply, "unknown mode") {
			t.Errorf("expected unknown-mode message, got %q", reply)
		}
	})

	t.Run("source focus and all", func(t *testing.T) {
		st := &replState{mode: "fast", source: ""}
		handleReplCommand(st, sources, ":source k8s")
		if st.source != "k8s" {
			t.Fatalf("source = %q, want k8s", st.source)
		}
		handleReplCommand(st, sources, ":source all")
		if st.source != "" {
			t.Errorf("source = %q, want empty (all)", st.source)
		}
	})

	t.Run("reject unknown source", func(t *testing.T) {
		st := &replState{mode: "fast", source: "k8s"}
		handleReplCommand(st, sources, ":source nope")
		if st.source != "k8s" {
			t.Errorf("unknown source changed state to %q", st.source)
		}
	})

	t.Run("query and index models", func(t *testing.T) {
		st := &replState{mode: "balanced"}
		handleReplCommand(st, sources, "/query-model ollama/qwen2.5:14b")
		if st.model != "ollama/qwen2.5:14b" {
			t.Errorf("query model = %q", st.model)
		}
		handleReplCommand(st, sources, "/index-model ollama/llama3.1:8b")
		if st.indexModel != "ollama/llama3.1:8b" {
			t.Errorf("index model = %q", st.indexModel)
		}
		handleReplCommand(st, sources, "/query-model") // bare arg clears to config
		if st.model != "" {
			t.Errorf("query model not cleared: %q", st.model)
		}
	})

	t.Run("legacy colon prefix still works", func(t *testing.T) {
		st := &replState{mode: "balanced"}
		handleReplCommand(st, sources, ":mode quality")
		if st.mode != "quality" {
			t.Errorf("colon prefix mode = %q, want quality", st.mode)
		}
	})

	t.Run("toggle retrieve-only", func(t *testing.T) {
		st := &replState{mode: "fast"}
		handleReplCommand(st, sources, ":retrieve-only")
		if !st.retrieveOnly {
			t.Error("retrieve-only should toggle on")
		}
	})
}

func TestParseWorkspaceSwitch(t *testing.T) {
	if p, ok := parseWorkspaceSwitch("/workspace ~/grove-bench/ws"); !ok || p != "~/grove-bench/ws" {
		t.Errorf("workspace parse = %q, %v", p, ok)
	}
	if p, ok := parseWorkspaceSwitch("/forest /tmp/other"); !ok || p != "/tmp/other" {
		t.Errorf("forest alias parse = %q, %v", p, ok)
	}
	if _, ok := parseWorkspaceSwitch("/mode fast"); ok {
		t.Error("/mode should not parse as a workspace switch")
	}
	if _, ok := parseWorkspaceSwitch("what is a pod"); ok {
		t.Error("a question should not parse as a workspace switch")
	}
}

func TestMatchCommands(t *testing.T) {
	if got := matchCommands("/query"); len(got) != 1 || got[0].name != "/query-model" {
		t.Errorf("/query should match only /query-model, got %v", got)
	}
	if got := matchCommands("/qu"); len(got) != 2 { // /query-model and /quit
		t.Errorf("/qu should match 2 commands, got %d", len(got))
	}
	if got := matchCommands("/"); len(got) != len(replCommands) {
		t.Errorf("/ should match all %d commands, got %d", len(replCommands), len(got))
	}
}

func TestCommandOptions(t *testing.T) {
	srcs := []string{"k8s", "notes"}
	models := []string{"ollama/llama3.1:8b"}
	if got := commandOptions("/mode", srcs, models); len(got) != 4 || got[0] != "fast" {
		t.Errorf("/mode options = %v", got)
	}
	if got := commandOptions("/source", srcs, models); len(got) != 3 || got[0] != "all" {
		t.Errorf("/source options = %v (want all + sources)", got)
	}
	if got := commandOptions("/index-model", srcs, models); len(got) != 1 || got[0] != "ollama/llama3.1:8b" {
		t.Errorf("/index-model options = %v", got)
	}
	if got := commandOptions("/workspace", srcs, models); got != nil {
		t.Errorf("/workspace is free-text, want nil options, got %v", got)
	}
	if got := commandOptions("/quit", srcs, models); got != nil {
		t.Errorf("/quit takes no args, want nil, got %v", got)
	}
}

func TestReplPrompt(t *testing.T) {
	if got := replPrompt(&replState{mode: "quality", source: ""}); got != "grove(quality·all)> " {
		t.Errorf("prompt = %q", got)
	}
	if got := replPrompt(&replState{mode: "fast", source: "k8s"}); got != "grove(fast·k8s)> " {
		t.Errorf("prompt = %q", got)
	}
}
