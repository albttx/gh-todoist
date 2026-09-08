package todoist

import "testing"

// TestPriorityInversion pins the single most dangerous fact about this API:
// Todoist's numeric priority runs backwards from its user-facing names, and
// Todoist's own request documentation states it the wrong way round. If this
// test ever fails because someone "fixed" the constants, every urgent issue
// will silently land in Todoist as a normal-priority task.
func TestPriorityInversion(t *testing.T) {
	t.Parallel()
	if PriorityUrgent != 4 {
		t.Errorf("PriorityUrgent = %d, want 4 (API 4 is the UI's p1)", PriorityUrgent)
	}
	if PriorityNormal != 1 {
		t.Errorf("PriorityNormal = %d, want 1 (API 1 is the UI's p4)", PriorityNormal)
	}
	if PriorityUrgent <= PriorityNormal {
		t.Error("urgent must be numerically greater than normal")
	}
}

func TestPriorityFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		labels []string
		want   int
	}{
		{name: "no labels", labels: nil, want: PriorityNormal},
		{name: "unknown label", labels: []string{"docs", "good first issue"}, want: PriorityNormal},
		{name: "p0", labels: []string{"p0"}, want: PriorityUrgent},
		{name: "critical", labels: []string{"critical"}, want: PriorityUrgent},
		{name: "security", labels: []string{"security"}, want: PriorityUrgent},
		{name: "urgent", labels: []string{"urgent"}, want: PriorityUrgent},
		{name: "p1", labels: []string{"p1"}, want: PriorityHigh},
		{name: "bug", labels: []string{"bug"}, want: PriorityHigh},
		{name: "p2", labels: []string{"p2"}, want: PriorityMedium},
		{name: "enhancement", labels: []string{"enhancement"}, want: PriorityMedium},
		{name: "feature", labels: []string{"feature"}, want: PriorityMedium},
		{name: "case insensitive", labels: []string{"Security"}, want: PriorityUrgent},
		{name: "whitespace tolerated", labels: []string{"  bug  "}, want: PriorityHigh},
		{name: "most urgent label wins", labels: []string{"enhancement", "bug", "p0"}, want: PriorityUrgent},
		{name: "known plus unknown", labels: []string{"triage", "bug"}, want: PriorityHigh},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := PriorityFor(tt.labels); got != tt.want {
				t.Errorf("PriorityFor(%v) = %d, want %d", tt.labels, got, tt.want)
			}
		})
	}
}
