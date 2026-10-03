package sandbox

import (
	"strings"
	"testing"
)

func TestRangeReadPreservesSourceProvenance(t *testing.T) {
	w, _ := testWorkspace(t, Limits{MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxListedFiles: 20, MaxSearchHits: 20})
	index := NewSourceIndex(w, strings.Repeat("a", 40))
	rangeRead, err := index.ReadRange("internal/workload/worker.go", 2, 2, "")
	if err != nil || rangeRead.StartLine != 2 || rangeRead.EndLine != 2 || rangeRead.Revision != strings.Repeat("a", 40) || len(rangeRead.Digest) != 64 || rangeRead.Text != "func process() {}" || !rangeRead.Untrusted {
		t.Fatalf("range provenance: %+v %v", rangeRead, err)
	}
	if _, err := index.ReadRange(rangeRead.Path, 1, 2, strings.Repeat("f", 64)); err == nil {
		t.Fatal("accepted stale content digest")
	}
	for _, bounds := range [][2]int{{0, 2}, {2, 1}, {1, 501}, {50, 51}} {
		if _, err := index.ReadRange(rangeRead.Path, bounds[0], bounds[1], ""); err == nil {
			t.Fatalf("accepted invalid bounds %v", bounds)
		}
	}
}

func TestRepeatedSearchReusesPinnedIndex(t *testing.T) {
	w, _ := testWorkspace(t, Limits{MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxListedFiles: 20, MaxSearchHits: 20})
	index := NewSourceIndex(w, strings.Repeat("a", 40))
	for i := 0; i < 40; i++ {
		hits, _, err := index.Search("internal/workload", "process", 0, 1)
		if err != nil || len(hits) != 1 || hits[0].Line != 2 {
			t.Fatalf("indexed search %d: %v %v", i, hits, err)
		}
	}
	if index.Reads != 1 || index.BytesRead != int64(len("package workload\nfunc process() {}\n")) {
		t.Fatalf("repeated searches reread source: %d %d", index.Reads, index.BytesRead)
	}
	other := NewSourceIndex(w, strings.Repeat("b", 40))
	read, err := other.ReadRange("internal/workload/worker.go", 1, 2, "")
	if err != nil || read.Revision == index.revision || other.Reads != 1 {
		t.Fatalf("revision isolation: %+v %v", read, err)
	}
}
