package task

import "testing"

func TestParseStatus(t *testing.T) {
	for in, want := range map[string]bool{
		"todo": true, "in_progress": true, "done": true,
		"": false, "closed": false, "DONE": false, " todo": false,
	} {
		st, ok := ParseStatus(in)
		if ok != want || (ok && string(st) != in) || (!ok && st != "") {
			t.Errorf("ParseStatus(%q) = %q, %v", in, st, ok)
		}
	}
}

func TestErrorTexts(t *testing.T) {
	if got := (&VersionConflictError{Expected: 1, Current: 2}).Error(); got != "task: version conflict: expected 1, current 2" {
		t.Errorf("VersionConflictError: %q", got)
	}
}
