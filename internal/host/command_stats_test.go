package host

import (
	"encoding/json"
	"testing"
)

func TestQueryLogStatsBuildsReadOnlyStatsFlag(t *testing.T) {
	spec, ok := operationSpec("query_log")
	if !ok {
		t.Fatal("query_log is not allowlisted")
	}
	if spec.write {
		t.Fatal("local stats must stay read-only")
	}
	args, err := spec.build(json.RawMessage(`{"stats":true,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"query-log", "--limit=1", "--stats"}
	if len(args) != len(want) {
		t.Fatalf("got %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("got %v, want %v", args, want)
		}
	}
}
