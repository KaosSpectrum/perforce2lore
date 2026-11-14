package lore

import "testing"

func TestParseStatus(t *testing.T) {
	out := `not an event
{"tagName":"repositoryStatusRevision","data":{"isLocalAhead":2}}
{"tagName":"repositoryStatusFile","data":{"flagStaged":true}}
{"tagName":"repositoryStatusFile","data":{"flagStaged":false}}
{"tagName":"repositoryStatusFile","data":{"flagStaged":true}}`
	s := ParseStatus(ParseEvents(out))
	if s.Staged != 2 || !s.Ahead {
		t.Fatalf("got %+v", s)
	}
}

func TestCompletionErrorMatchesExitCode(t *testing.T) {
	evs := ParseEvents(`{"tagName":"complete","data":{"status":89,"error":{"message":"Repository not found"}}}`)
	if got := CompletionError(evs, 89); got != "Repository not found (89)" {
		t.Fatalf("got %q", got)
	}
	if got := CompletionError(evs, 1); got != "" {
		t.Fatalf("got %q", got)
	}
}
