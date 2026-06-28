package util

import (
	"strings"
	"testing"
)

func TestPlanPost_Single(t *testing.T) {
	t.Parallel()
	p := PlanPost("hello")
	if p.Kind != PostKindSingle || p.Text != "hello" {
		t.Fatalf("got %+v", p)
	}
}

func TestPlanPost_Multi(t *testing.T) {
	t.Parallel()
	// Build 6000 chars of paragraphs so we land in (ShortLimit, LongLimit].
	body := strings.Repeat("paragraph\n\n", 600)
	p := PlanPost(body)
	if p.Kind != PostKindMulti {
		t.Fatalf("expected multi, got %v", p.Kind)
	}
	if len(p.Parts) < 2 {
		t.Fatalf("expected >=2 parts, got %d", len(p.Parts))
	}
	for i, part := range p.Parts {
		footer := "(part "
		if !strings.Contains(part, footer) {
			t.Fatalf("part %d missing footer: %q", i, part)
		}
	}
}

func TestPlanPost_File(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", LongLimit+10)
	p := PlanPost(body)
	if p.Kind != PostKindFile {
		t.Fatalf("expected file, got %v", p.Kind)
	}
	if p.Text != body {
		t.Fatalf("file text mutated")
	}
}

func TestSplitIntoChunks_HardSlice(t *testing.T) {
	t.Parallel()
	// Single line longer than soft limit must be hard-sliced.
	line := strings.Repeat("a", 300)
	chunks := SplitIntoChunks(line, 100)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 100 {
			t.Fatalf("chunk over soft limit: %d", len(c))
		}
	}
}

func TestSplitIntoChunks_ParagraphBoundary(t *testing.T) {
	t.Parallel()
	body := "alpha\n\nbeta\n\ngamma"
	chunks := SplitIntoChunks(body, 100)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk (all fits), got %d: %#v", len(chunks), chunks)
	}
}
