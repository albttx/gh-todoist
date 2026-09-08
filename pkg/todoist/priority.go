package todoist

import "strings"

// Todoist priority is INVERTED relative to its user-facing names: the API's 4 is
// the UI's p1 (urgent) and the API's 1 is the UI's p4 (normal). Todoist's own
// request documentation states this backwards. TestPriorityInversion pins it.
const (
	PriorityUrgent = 4 // shown as p1
	PriorityHigh   = 3 // shown as p2
	PriorityMedium = 2 // shown as p3
	PriorityNormal = 1 // shown as p4
)

// priorityByLabel maps a lower-cased GitHub label to a Todoist priority. These
// defaults are deliberately not configurable yet; configuration is deferred
// until a second opinion about the mapping actually exists.
var priorityByLabel = map[string]int{
	"p0":       PriorityUrgent,
	"critical": PriorityUrgent,
	"security": PriorityUrgent,
	"urgent":   PriorityUrgent,

	"p1":  PriorityHigh,
	"bug": PriorityHigh,

	"p2":          PriorityMedium,
	"enhancement": PriorityMedium,
	"feature":     PriorityMedium,
}

// PriorityFor maps a GitHub issue's labels to a Todoist priority. When several
// labels match, the most urgent wins. Unknown labels yield PriorityNormal.
func PriorityFor(labels []string) int {
	best := PriorityNormal
	for _, l := range labels {
		if p, ok := priorityByLabel[strings.ToLower(strings.TrimSpace(l))]; ok && p > best {
			best = p
		}
	}
	return best
}
