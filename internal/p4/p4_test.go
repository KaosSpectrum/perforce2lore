package p4

import "testing"

func TestParseRecordsDropsWarnings(t *testing.T) {
	out := `{"change":"12","user":"sam","client":"sam-pc","time":"1700000000","desc":"b"}
{"code":"error","data":"file(s) up-to-date.","severity":2,"generic":17}
{"change":"11","user":"alex","client":"alex-pc","time":"1600000000","desc":"a"}
`
	recs, failure, err := ParseRecords(out)
	if err != nil || failure != "" || len(recs) != 2 {
		t.Fatalf("got %d records, %q, %v", len(recs), failure, err)
	}
	changes := ParseChanges(recs)
	if changes[0].Number != 11 || changes[1].Workspace != "sam-pc" || changes[1].Time != 1700000000 {
		t.Fatalf("got %+v", changes)
	}
}

func TestParseRecordsReportsErrors(t *testing.T) {
	_, failure, _ := ParseRecords(`{"code":"error","data":"Perforce password (P4PASSWD) invalid or unset.\n","severity":3,"generic":36}`)
	if failure != "Perforce password (P4PASSWD) invalid or unset." {
		t.Fatalf("got %q", failure)
	}
}

func TestWorkspaceOptions(t *testing.T) {
	w := WorkspaceSpec{Options: []string{"noallwrite", "clobber", "rmdir"}}
	if !w.HasOption("RMDIR") || w.HasOption("allwrite") {
		t.Fatal("option lookup is wrong")
	}
}

func TestSetSpecBlockReplacesTheView(t *testing.T) {
	spec := "Client:\timport\n\nRoot:\t/w\n\nView:\n\t//depot/main/... //import/...\n\t-//depot/main/tmp/... //import/tmp/...\n\nChangeView:\n"
	got := SetSpecBlock(spec, "View", []string{viewLine("//depot/rel 1/...", "import")})
	want := "Client:\timport\n\nRoot:\t/w\n\n\nChangeView:\n\nView:\n\t\"//depot/rel 1/...\" //import/...\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestMapsChecksTheWholeView(t *testing.T) {
	w := WorkspaceSpec{View: []string{`"//depot/rel 1/..." //import/...`}}
	if !w.Maps("//depot/rel 1/...") || w.Maps("//depot/main/...") {
		t.Fatal("quoted view not matched")
	}
	w.View = append(w.View, "//depot/other/... //import/other/...")
	if w.Maps("//depot/rel 1/...") {
		t.Fatal("a view with extra lines should not match")
	}
}
