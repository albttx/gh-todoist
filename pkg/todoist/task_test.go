package todoist

import (
	"strings"
	"testing"
)

func TestIssueInputContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   IssueInput
		want string
	}{
		{
			name: "link plus title",
			in: IssueInput{
				URL:   "https://github.com/acme/widgets/issues/812",
				Ref:   "acme/widgets#812",
				Title: "fix session token rotation",
			},
			want: "[acme/widgets#812](https://github.com/acme/widgets/issues/812) fix session token rotation",
		},
		{
			name: "empty title leaves a bare link",
			in: IssueInput{
				URL: "https://github.com/o/r/issues/1",
				Ref: "o/r#1",
			},
			want: "[o/r#1](https://github.com/o/r/issues/1)",
		},
		{
			name: "title is trimmed",
			in: IssueInput{
				URL:   "https://github.com/o/r/issues/1",
				Ref:   "o/r#1",
				Title: "  padded  ",
			},
			want: "[o/r#1](https://github.com/o/r/issues/1) padded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.Content(); got != tt.want {
				t.Errorf("Content() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIssueInputDescription(t *testing.T) {
	t.Parallel()

	t.Run("short body passes through", func(t *testing.T) {
		t.Parallel()
		in := IssueInput{Body: "  a short body\n"}
		if got := in.Description(); got != "a short body" {
			t.Errorf("Description() = %q", got)
		}
	})

	t.Run("crlf normalised", func(t *testing.T) {
		t.Parallel()
		in := IssueInput{Body: "line one\r\nline two"}
		if got := in.Description(); got != "line one\nline two" {
			t.Errorf("Description() = %q", got)
		}
	})

	t.Run("long body truncated with an ellipsis", func(t *testing.T) {
		t.Parallel()
		in := IssueInput{Body: strings.Repeat("x", DescriptionLimit+50)}
		got := in.Description()
		if !strings.HasSuffix(got, "…") {
			t.Error("expected a trailing ellipsis")
		}
		if n := len([]rune(strings.TrimSuffix(got, "…"))); n != DescriptionLimit {
			t.Errorf("kept %d runes, want %d", n, DescriptionLimit)
		}
	})

	t.Run("truncation counts runes not bytes", func(t *testing.T) {
		t.Parallel()
		// Multi-byte runes would break a byte-based slice mid-character.
		in := IssueInput{Body: strings.Repeat("é", DescriptionLimit+10)}
		got := in.Description()
		body := strings.TrimSuffix(got, "…")
		if n := len([]rune(body)); n != DescriptionLimit {
			t.Errorf("kept %d runes, want %d", n, DescriptionLimit)
		}
		if strings.Contains(body, "�") {
			t.Error("truncation split a multi-byte rune")
		}
	})

	t.Run("exactly at the limit is not truncated", func(t *testing.T) {
		t.Parallel()
		in := IssueInput{Body: strings.Repeat("y", DescriptionLimit)}
		if got := in.Description(); strings.HasSuffix(got, "…") {
			t.Error("a body exactly at the limit must not be marked truncated")
		}
	})
}

func TestBuildItemAdd(t *testing.T) {
	t.Parallel()

	in := IssueInput{
		URL:          "https://github.com/acme/widgets/issues/812",
		Ref:          "acme/widgets#812",
		Title:        "fix session token rotation",
		Body:         "the token is not rotated on reauth",
		Labels:       []string{"bug", "backend"},
		MilestoneDue: "2026-10-01",
	}
	cmd := BuildItemAdd(in, "proj0000000001", "gh")

	if cmd.Type != "item_add" {
		t.Errorf("Type = %q", cmd.Type)
	}
	if cmd.UUID != CommandUUID(in.URL) {
		t.Error("uuid must be derived from the issue URL, or idempotency is lost")
	}
	args, ok := cmd.Args.(ItemAddArgs)
	if !ok {
		t.Fatalf("Args = %#v", cmd.Args)
	}
	if args.ProjectID != "proj0000000001" {
		t.Errorf("ProjectID = %q", args.ProjectID)
	}
	if len(args.Labels) != 1 || args.Labels[0] != "gh" {
		t.Errorf("Labels = %v, want the configured label by name", args.Labels)
	}
	if args.Priority != PriorityHigh {
		t.Errorf("Priority = %d, want %d for a bug", args.Priority, PriorityHigh)
	}
	if args.Due == nil || args.Due.Date != "2026-10-01" {
		t.Errorf("Due = %#v, want the milestone date", args.Due)
	}
}

func TestBuildItemAddWithoutMilestoneOrProject(t *testing.T) {
	t.Parallel()
	cmd := BuildItemAdd(IssueInput{
		URL: "https://github.com/o/r/issues/1",
		Ref: "o/r#1",
	}, "", "gh")

	args := cmd.Args.(ItemAddArgs)
	if args.Due != nil {
		t.Error("no milestone must mean no due object")
	}
	if args.ProjectID != "" {
		t.Error("an empty project must be omitted so Todoist uses the Inbox")
	}
	if args.Priority != PriorityNormal {
		t.Errorf("Priority = %d, want normal", args.Priority)
	}
}

func TestBuildItemAddGenerationChangesUUID(t *testing.T) {
	t.Parallel()
	in := IssueInput{URL: "https://github.com/o/r/issues/1", Ref: "o/r#1"}
	first := BuildItemAdd(in, "", "gh")
	in.Generation = 1
	revived := BuildItemAdd(in, "", "gh")
	if first.UUID == revived.UUID {
		t.Fatal("a revive must use a fresh uuid, otherwise Todoist refuses it as a duplicate")
	}
	if first.TempID == revived.TempID {
		t.Error("temp ids should differ per generation too")
	}
}
