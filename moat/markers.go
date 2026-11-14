package moat

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ignorable lists the default-ignorable and format code points removed before
// matching, as inclusive ranges. Same table in every Moat implementation.
var ignorable = [][2]rune{
	{0x00AD, 0x00AD}, {0x034F, 0x034F}, {0x061C, 0x061C}, {0x115F, 0x1160},
	{0x17B4, 0x17B5}, {0x180B, 0x180F}, {0x200B, 0x200F}, {0x202A, 0x202E},
	{0x2060, 0x206F}, {0x3164, 0x3164}, {0xFE00, 0xFE0F}, {0xFEFF, 0xFEFF},
	{0xFFA0, 0xFFA0}, {0xFFF0, 0xFFF8}, {0x1BCA0, 0x1BCA3}, {0x1D173, 0x1D17A},
	{0xE0000, 0xE0FFF},
}

func isIgnorable(r rune) bool {
	for _, rg := range ignorable {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// isHSpace reports a horizontal space: tab or any Unicode space separator.
func isHSpace(r rune) bool {
	return r == '\t' || unicode.Is(unicode.Zs, r)
}

// confusables maps Cyrillic and Greek look-alikes of the marker letters to the
// lowercase Latin letter. Same table in every Moat implementation; this is not
// general homoglyph detection.
var confusables = map[rune]rune{
	0x0430: 'a', // CYRILLIC SMALL LETTER A
	0x0410: 'a', // CYRILLIC CAPITAL LETTER A
	0x0441: 'c', // CYRILLIC SMALL LETTER ES
	0x0421: 'c', // CYRILLIC CAPITAL LETTER ES
	0x0435: 'e', // CYRILLIC SMALL LETTER IE
	0x0415: 'e', // CYRILLIC CAPITAL LETTER IE
	0x0456: 'i', // CYRILLIC SMALL LETTER BYELORUSSIAN-UKRAINIAN I
	0x0406: 'i', // CYRILLIC CAPITAL LETTER BYELORUSSIAN-UKRAINIAN I
	0x043E: 'o', // CYRILLIC SMALL LETTER O
	0x041E: 'o', // CYRILLIC CAPITAL LETTER O
	0x0440: 'p', // CYRILLIC SMALL LETTER ER
	0x0420: 'p', // CYRILLIC CAPITAL LETTER ER
	0x0455: 's', // CYRILLIC SMALL LETTER DZE
	0x0405: 's', // CYRILLIC CAPITAL LETTER DZE
	0x0443: 'y', // CYRILLIC SMALL LETTER U
	0x0423: 'y', // CYRILLIC CAPITAL LETTER U
	0x0501: 'd', // CYRILLIC SMALL LETTER KOMI DE
	0x04CF: 'l', // CYRILLIC SMALL LETTER PALOCHKA
	0x04C0: 'l', // CYRILLIC LETTER PALOCHKA
	0x0422: 't', // CYRILLIC CAPITAL LETTER TE
	0x041C: 'm', // CYRILLIC CAPITAL LETTER EM
	0x041D: 'h', // CYRILLIC CAPITAL LETTER EN
	0x0445: 'x', // CYRILLIC SMALL LETTER HA
	0x0425: 'x', // CYRILLIC CAPITAL LETTER HA
	0x0391: 'a', // GREEK CAPITAL LETTER ALPHA
	0x03B1: 'a', // GREEK SMALL LETTER ALPHA
	0x0395: 'e', // GREEK CAPITAL LETTER EPSILON
	0x03B5: 'e', // GREEK SMALL LETTER EPSILON
	0x0399: 'i', // GREEK CAPITAL LETTER IOTA
	0x03B9: 'i', // GREEK SMALL LETTER IOTA
	0x039F: 'o', // GREEK CAPITAL LETTER OMICRON
	0x03BF: 'o', // GREEK SMALL LETTER OMICRON
	0x03A1: 'p', // GREEK CAPITAL LETTER RHO
	0x03C1: 'p', // GREEK SMALL LETTER RHO
	0x03A4: 't', // GREEK CAPITAL LETTER TAU
	0x03C4: 't', // GREEK SMALL LETTER TAU
	0x03A5: 'y', // GREEK CAPITAL LETTER UPSILON
	0x03C5: 'u', // GREEK SMALL LETTER UPSILON
	0x03BD: 'v', // GREEK SMALL LETTER NU
	0x039C: 'm', // GREEK CAPITAL LETTER MU
	0x039D: 'n', // GREEK CAPITAL LETTER NU
	0x03A7: 'x', // GREEK CAPITAL LETTER CHI
	0x03C7: 'x', // GREEK SMALL LETTER CHI
	0x03F2: 'c', // GREEK LUNATE SIGMA SYMBOL
	0x03F9: 'c', // GREEK CAPITAL LUNATE SIGMA SYMBOL
	0x0397: 'h', // GREEK CAPITAL LETTER ETA
	0x0442: 't', // CYRILLIC SMALL LETTER TE
}

// stripMarks does NFKD and drops every nonspacing (Mn) and enclosing (Me) mark.
func stripMarks(s string) string {
	var sb strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// normalize prepares text for the marker scan, in this order: Unicode NFKC;
// NFKD with combining marks (Mn, Me) dropped; removal of the ignorable code
// points; mapping of the confusables to Latin; lower-casing; runs of horizontal
// whitespace collapsed to one space. Line breaks are kept because the label
// check is line-start. The result is used only for matching.
func normalize(s string) string {
	s = stripMarks(norm.NFKC.String(s))
	var sb strings.Builder
	space := false
	for _, r := range s {
		if isIgnorable(r) {
			continue
		}
		if isHSpace(r) {
			space = true
			continue
		}
		if space {
			sb.WriteByte(' ')
			space = false
		}
		if l, ok := confusables[r]; ok {
			r = l
		}
		switch r {
		case '\r', 0x85, 0x2028, 0x2029:
			r = '\n'
		}
		sb.WriteRune(unicode.ToLower(r))
	}
	if space {
		sb.WriteByte(' ')
	}
	return sb.String()
}

var (
	lineLabelRe = regexp.MustCompile(`(?m)^[ \t]*(system|assistant|developer|tool|user)[ \t]*:`)
	headingRe   = regexp.MustCompile(`###[ \t]*(system|instruction)`)
	roleKeyRe   = regexp.MustCompile(`"role"[ \t\n]*:[ \t\n]*"(system|developer|assistant)"`)
	markerTexts = []string{"<|system|>", "<|im_start|>", "<|im_end|>", "[inst]", "<<sys>>"}
)

// scanText looks for a marker in one piece of text and names it, or returns "".
func scanText(text string) string {
	n := normalize(text)
	if m := lineLabelRe.FindStringSubmatch(n); m != nil {
		return "a line starting with a role label"
	}
	for _, t := range markerTexts {
		if strings.Contains(n, t) {
			return "the marker " + t
		}
	}
	if headingRe.MatchString(n) {
		return "a system or instruction heading"
	}
	if roleKeyRe.MatchString(n) {
		return "a JSON role key set to system, developer or assistant"
	}
	return ""
}

// findMarker scans the whole input text, then walks a structured input so that
// each string and each object with a role key is checked on its own.
func findMarker(value any, text string) string {
	if m := scanText(text); m != "" {
		return m
	}
	if s, ok := value.(string); ok {
		// A string that is itself JSON: check its structure too.
		t := strings.TrimSpace(s)
		if len(t) > 0 && (t[0] == '{' || t[0] == '[') {
			if parsed, err := parseJSON([]byte(t)); err == nil {
				return walk(parsed)
			}
		}
		return ""
	}
	return walk(value)
}

func walk(v any) string {
	switch x := v.(type) {
	case string:
		return scanText(x)
	case []any:
		for _, e := range x {
			if m := walk(e); m != "" {
				return m
			}
		}
	case map[string]any:
		if r, ok := x["role"].(string); ok {
			switch strings.TrimSpace(normalize(r)) {
			case "system", "developer", "assistant":
				return "a JSON role key set to system, developer or assistant"
			}
		}
		for k, e := range x {
			if m := scanText(k); m != "" {
				return m
			}
			if m := walk(e); m != "" {
				return m
			}
		}
	}
	return ""
}
