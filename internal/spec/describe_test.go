package spec

import (
	"net/http"
	"strings"
	"testing"
)

func TestDescribeExampleFieldsPreferred(t *testing.T) {
	x := load(t)
	ex, _ := x.describe(x.byID["apps_appEvents_getToManyRelated"]).Example.(map[string]any)
	fields, _ := ex["fields"].(map[string]string)
	got := strings.Split(fields["appEvents"], ",")
	for _, want := range []string{"referenceName", "eventState"} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("example fields[appEvents] = %v, want %s", got, want)
		}
	}
	if len(got) > exampleFields {
		t.Errorf("example fields[appEvents] = %v, want at most %d", got, exampleFields)
	}
}

func TestDescribeExampleOmitsFieldsWithoutPreferred(t *testing.T) {
	x := load(t)
	checked := 0
	for id, op := range x.byID {
		if op.Method != http.MethodGet {
			continue
		}
		names := x.FieldNames(targetType(id))
		if len(names) == 0 || preferredFields(names) != nil {
			continue
		}
		checked++
		ex, _ := x.describe(op).Example.(map[string]any)
		if _, ok := ex["fields"]; ok {
			t.Errorf("%s: example has fields %v though %s has no preferred attributes", id, ex["fields"], targetType(id))
		}
	}
	if checked == 0 {
		t.Fatal("no GET operation whose type lacks preferred attributes; test exercises nothing")
	}
}
