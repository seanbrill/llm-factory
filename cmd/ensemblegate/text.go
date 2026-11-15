package main

import (
	"regexp"
	"strings"
)

// Text helpers: what chat history members get, and what speech members say.

var (
	reThink     = regexp.MustCompile(`(?s)<think>.*?(?:</think>|$)`)
	reDataURL   = regexp.MustCompile(`data:[\w/+.-]+;base64,[A-Za-z0-9+/=]+`)
	reMdMedia   = regexp.MustCompile(`!?\[([^\]]*)\]\((?:data:[^)]*|/api/media/[^)]*)\)`)
	reCodeBlock = regexp.MustCompile("(?s)```.*?(?:```|$)")
	reInline    = regexp.MustCompile("`([^`]*)`")
	reMdLink    = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	reURL       = regexp.MustCompile(`https?://\S+`)
	reHeading   = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	reBullet    = regexp.MustCompile(`(?m)^\s*(?:[-*+]|\d+[.)])\s+`)
	reEmphasis  = regexp.MustCompile(`[*_~]{1,3}`)
	reTable     = regexp.MustCompile(`(?m)^\s*\|.*\|\s*$`)
	reSpace     = regexp.MustCompile(`[ \t]+`)
	reLineSpace = regexp.MustCompile(` *\n *`)
	reBlank     = regexp.MustCompile(`\n{2,}`)
)

// stripThink drops a reasoning model's <think>…</think> block.
func stripThink(s string) string { return strings.TrimSpace(reThink.ReplaceAllString(s, "")) }

// historyText is an assistant turn as members should see it again: generated
// media (multi-MB data URLs) replaced by a short note, reasoning dropped.
func historyText(s string) string {
	s = reMdMedia.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "!") {
			return "[generated image]"
		}
		return "[generated media]"
	})
	return stripThink(reDataURL.ReplaceAllString(s, "[media]"))
}

// maxSpoken caps how much of a reply is spoken: long answers take a long time
// to synthesize; the full text is in the chat.
const maxSpoken = 1200

// speakable turns a chat reply into text to speak: no reasoning, code, links,
// media or markdown; trimmed to whole sentences within maxSpoken.
func speakable(s string) string {
	s = stripThink(s)
	s = reCodeBlock.ReplaceAllString(s, " (The code is in the chat.) ")
	s = reMdMedia.ReplaceAllString(s, "")
	s = reDataURL.ReplaceAllString(s, "")
	s = reTable.ReplaceAllString(s, "")
	s = reMdLink.ReplaceAllString(s, "$1")
	s = reURL.ReplaceAllString(s, "")
	s = reInline.ReplaceAllString(s, "$1")
	s = reHeading.ReplaceAllString(s, "")
	s = reBullet.ReplaceAllString(s, "")
	s = reEmphasis.ReplaceAllString(s, "")
	s = reSpace.ReplaceAllString(s, " ")
	s = reLineSpace.ReplaceAllString(s, "\n")
	s = strings.TrimSpace(reBlank.ReplaceAllString(s, "\n"))
	if len(s) <= maxSpoken {
		return s
	}
	cut := s[:maxSpoken]
	if i := strings.LastIndexAny(cut, ".!?"); i > maxSpoken/3 {
		return cut[:i+1]
	}
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		return cut[:i] + "…"
	}
	return strings.ToValidUTF8(cut, "")
}
