package serialize

import (
	"fmt"
	"regexp"
	"strings"
)

// FailClosed is the policy prose's fail-closed sentence. Arm A's registered
// prose ablation removes it and nothing else.
const FailClosed = "When a rule depends on something you cannot determine, treat it as the unsafe case and deny."

// failClosed matches FailClosed however the prose wraps it, with the
// whitespace that joins it to the sentence before it on the same paragraph.
var failClosed = regexp.MustCompile(`[ \t]*\n?[ \t]*` + strings.Join(
	strings.Fields(regexp.QuoteMeta(FailClosed)), `\s+`))

// WithoutFailClosed returns the policy prose with the fail-closed sentence
// and the space or line break before it removed; the rest of the paragraph,
// and what follows the sentence, is kept as written. It fails unless the
// sentence occurs exactly once.
func WithoutFailClosed(prose string) (string, error) {
	locs := failClosed.FindAllStringIndex(prose, -1)
	if len(locs) != 1 {
		return "", fmt.Errorf("policy prose has %d copies of the fail-closed sentence, want 1", len(locs))
	}
	return prose[:locs[0][0]] + prose[locs[0][1]:], nil
}
