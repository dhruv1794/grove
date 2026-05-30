package grove

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"grove/internal/llm"
)

const (
	// chunkSimDrop: a new passage starts when the cosine similarity between two
	// adjacent sentence embeddings falls below this — a topical shift. Tunable;
	// the Frontiers (2025) semantic-chunking work used ~0.7 on bge-m3, but that
	// over-splits dense prose here, so we start lower.
	chunkSimDrop = 0.5
	// chunkMaxChars hard-caps a passage so a long topically-uniform run still
	// splits (and stays within the embedder budget).
	chunkMaxChars = 1200
)

// splitSentences breaks text into rough sentence units for boundary detection:
// split on newlines (markdown headings/paragraphs are natural breaks), then on
// sentence-terminating punctuation. Byte-scan on ASCII .!? is UTF-8-safe (multi-
// byte runes never collide with those bytes).
func splitSentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		start := 0
		for i := 0; i < len(line); i++ {
			if c := line[i]; c == '.' || c == '!' || c == '?' {
				j := i + 1
				for j < len(line) && (line[j] == '.' || line[j] == '!' || line[j] == '?') {
					j++
				}
				if seg := strings.TrimSpace(line[start:j]); seg != "" {
					out = append(out, seg)
				}
				start = j
				i = j - 1
			}
		}
		if seg := strings.TrimSpace(line[start:]); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// semanticChunks splits text into passages by embedding its sentences and
// starting a new chunk where adjacent-sentence similarity drops below the
// threshold or the chunk would exceed chunkMaxChars. A single-sentence (or
// unembeddable) input returns the whole text as one chunk. label identifies the
// source doc in fallback warnings.
func semanticChunks(ctx context.Context, emb *llm.Embedder, label, text string) ([]string, error) {
	sents := splitSentences(text)
	if len(sents) <= 1 {
		return []string{strings.TrimSpace(text)}, nil
	}
	vecs, err := embedSentences(ctx, emb, sents)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(sents) {
		fmt.Fprintf(os.Stderr,
			"warning: embedder returned %d vectors for %d sentences of %s — falling back to a single chunk\n",
			len(vecs), len(sents), label)
		return []string{strings.TrimSpace(text)}, nil
	}
	var chunks, cur []string
	curLen := 0
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, strings.Join(cur, " "))
			cur, curLen = nil, 0
		}
	}
	for i, s := range sents {
		boundary := i > 0 && cosine32(vecs[i-1], vecs[i]) < chunkSimDrop
		if curLen+len(s) > chunkMaxChars && len(cur) > 0 {
			boundary = true
		}
		if boundary {
			flush()
		}
		cur = append(cur, s)
		curLen += len(s) + 1
	}
	flush()
	return chunks, nil
}

// embedSentences embeds sentences in fixed-size batches so a long document
// doesn't issue one oversized request (timeout / context-limit risk). On a
// short read it returns what it has; the caller treats a length mismatch as a
// fallback signal.
func embedSentences(ctx context.Context, emb *llm.Embedder, sents []string) ([][]float32, error) {
	vecs := make([][]float32, 0, len(sents))
	for start := 0; start < len(sents); start += embedBatch {
		end := min(start+embedBatch, len(sents))
		batch, err := emb.Embed(ctx, sents[start:end])
		if err != nil {
			return nil, err
		}
		vecs = append(vecs, batch...)
	}
	return vecs, nil
}

func cosine32(a, b []float32) float64 {
	n := min(len(a), len(b))
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
