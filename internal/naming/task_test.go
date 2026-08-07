package naming

import "testing"

func TestTaskName(t *testing.T) {
	tests := []struct {
		name, title, id, want string
	}{
		{"normal title", "Fix concurrent invitation acceptance", "a2e2b9cdef", "fix-concurrent-invitation-acceptance-a2e2b9cd"},
		{"punctuation", "Fix: API / auth (v2)!", "ABCD-1234", "fix-api-auth-v2-abcd1234"},
		{"long title", "This title is deliberately much longer than forty characters to verify truncation", "id123456789", "this-title-is-deliberately-much-longer-id123456"},
		{"empty title", " --- ", "a2e2b9", "task-a2e2b9"},
		{"empty values", "", "", "task-unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := TaskName(test.title, test.id); got != test.want {
				t.Fatalf("TaskName(%q, %q) = %q, want %q", test.title, test.id, got, test.want)
			}
		})
	}
}

func TestTaskNameSeparatesSameTitle(t *testing.T) {
	first := TaskName("Resolve flaky test", "a2e2b9cdef")
	second := TaskName("Resolve flaky test", "b3f3c0ddef")
	if first == second {
		t.Fatalf("same-title task names collided: %q", first)
	}
}
