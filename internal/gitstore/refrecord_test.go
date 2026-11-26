package gitstore

import (
	"errors"
	"reflect"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Every for-each-ref listing written with taskRefFormat is split by the one
// reader: records in Git's order, refusals naming the refs listed, and the
// first refusal from a record ends the walk.
func TestEachRefRecordSplitsAListing(t *testing.T) {
	t.Parallel()

	var got [][3]string
	listing := []byte("refs/a\x00111\x00\nrefs/b\x00222\x00refs/a\n")
	if err := eachRefRecord(listing, "relic ref", func(name, objectID, target string) error {
		got = append(got, [3]string{name, objectID, target})
		return nil
	}); err != nil {
		t.Fatalf("eachRefRecord() error = %v", err)
	}
	if want := [][3]string{{"refs/a", "111", ""}, {"refs/b", "222", "refs/a"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("eachRefRecord() visited %q, want %q", got, want)
	}
	if err := eachRefRecord(nil, "relic ref", func(string, string, string) error {
		t.Fatal("an empty listing has no record to visit")
		return nil
	}); err != nil {
		t.Fatalf("eachRefRecord(empty) error = %v", err)
	}

	for name, test := range map[string]struct {
		listing string
		want    string
	}{
		"unterminated":     {listing: "refs/a\x00111\x00", want: "Git returned an unterminated task ref record"},
		"two fields":       {listing: "refs/a\x00111\n", want: "Git returned an invalid task ref record"},
		"no name":          {listing: "\x00111\x00\n", want: "Git returned an invalid task ref record"},
		"no object ID":     {listing: "refs/a\x00\x00\n", want: "Git returned an invalid task ref record"},
		"bad second entry": {listing: "refs/a\x00111\x00\nrefs/b\n", want: "Git returned an invalid task ref record"},
	} {
		err := eachRefRecord([]byte(test.listing), "task ref", func(string, string, string) error { return nil })
		if core.CategoryOf(err) != core.CategoryCorruptData || err.Error() != test.want {
			t.Errorf("%s: eachRefRecord() error = %v, want corrupt data %q", name, err, test.want)
		}
	}

	stop := errors.New("stop")
	visited := 0
	err := eachRefRecord([]byte("refs/a\x00111\x00\nrefs/b\x00222\x00\n"), "task ref", func(string, string, string) error {
		visited++
		return stop
	})
	if !errors.Is(err, stop) || visited != 1 {
		t.Fatalf("eachRefRecord() = %v after %d records, want the visitor's error after 1", err, visited)
	}
}
