package updates

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	versionRE    = regexp.MustCompile(`\d+(?:\.\d+)+`)
	prereleaseRE = regexp.MustCompile(`(?i)(alpha|beta|rc|pre|dev|nightly)`)
)

// ExtractVersion finds the first dotted version number in s ("AdGuard Home, version v0.107.79"
// gives "0.107.79"), or "" when there is none.
func ExtractVersion(s string) string { return versionRE.FindString(s) }

// Compare orders dotted version numbers part by part (-1, 0 or 1); missing parts count as 0.
func Compare(a, b string) int {
	as, bs := strings.Split(ExtractVersion(a), "."), strings.Split(ExtractVersion(b), ".")
	for i := range max(len(as), len(bs)) {
		x, y := part(as, i), part(bs, i)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

func part(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(parts[i])
	return n
}
