package util

import (
	"fmt"
	"regexp"
)

// Slack chat.update has a hard ~4000-char ceiling. We sit well below so the
// footer added to multi-part posts never pushes us over.
const (
	ShortLimit     = 3000
	LongLimit      = 20000
	chunkSoftLimit = 2900
)

// PostKind enumerates how a sanitized payload should be delivered.
type PostKind int

const (
	PostKindSingle PostKind = iota
	PostKindMulti
	PostKindFile
)

// PostPlan describes how to deliver a sanitized payload back to Slack:
//
//	Single  → one chat.update overwriting the progress message
//	Multi   → chat.update for part 1, chat.postMessage for parts 2..n
//	File    → delete the progress message, files.uploadV2 the full text
type PostPlan struct {
	Kind  PostKind
	Text  string   // populated for Single / File
	Parts []string // populated for Multi (each already carries a "(part k/n)" footer)
}

// PlanPost decides the delivery strategy for `raw` based on its length.
func PlanPost(raw string) PostPlan {
	n := len(raw)
	switch {
	case n == 0:
		return PostPlan{Kind: PostKindSingle, Text: ""}
	case n <= ShortLimit:
		return PostPlan{Kind: PostKindSingle, Text: raw}
	case n > LongLimit:
		return PostPlan{Kind: PostKindFile, Text: raw}
	}
	chunks := SplitIntoChunks(raw, chunkSoftLimit)
	total := len(chunks)
	parts := make([]string, total)
	for i, c := range chunks {
		parts[i] = fmt.Sprintf("%s\n\n(part %d/%d)", c, i+1, total)
	}
	return PostPlan{Kind: PostKindMulti, Parts: parts}
}

var paragraphSep = regexp.MustCompile(`\n\n+`)

// SplitIntoChunks splits text along paragraph (\n\n) boundaries without
// exceeding softLimit. Paragraphs larger than softLimit fall back to line
// boundaries, then to a hard slice.
func SplitIntoChunks(input string, softLimit int) []string {
	if softLimit <= 0 {
		return []string{input}
	}
	paragraphs := paragraphSep.Split(input, -1)
	var chunks []string
	var buf string

	flush := func() {
		if buf != "" {
			chunks = append(chunks, buf)
			buf = ""
		}
	}

	pushSegment := func(segment string) {
		if segment == "" {
			return
		}
		if len(segment) > softLimit {
			// Paragraph too big — fall back to lines.
			lineBuf := ""
			for _, line := range splitLines(segment) {
				candidate := line
				if lineBuf != "" {
					candidate = lineBuf + "\n" + line
				}
				if len(candidate) > softLimit {
					if lineBuf != "" {
						chunks = append(chunks, lineBuf)
						lineBuf = ""
					}
					if len(line) > softLimit {
						for i := 0; i < len(line); i += softLimit {
							end := i + softLimit
							if end > len(line) {
								end = len(line)
							}
							chunks = append(chunks, line[i:end])
						}
					} else {
						lineBuf = line
					}
				} else {
					lineBuf = candidate
				}
			}
			if lineBuf != "" {
				chunks = append(chunks, lineBuf)
			}
			return
		}
		candidate := segment
		if buf != "" {
			candidate = buf + "\n\n" + segment
		}
		if len(candidate) > softLimit {
			flush()
			buf = segment
		} else {
			buf = candidate
		}
	}

	for _, p := range paragraphs {
		pushSegment(p)
	}
	flush()
	return chunks
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
