package httpx

import (
	"net/url"
	"reflect"
	"testing"
)

func TestRuntimeFileBrowseQueryPreservesCrossPlatformPaths(t *testing.T) {
	for _, test := range []struct {
		name  string
		input url.Values
	}{
		{
			name: "posix host",
			input: url.Values{
				"path": {"/srv/projects/with spaces"}, "offset": {"20"}, "limit": {"200"}, "include_hidden": {"true"},
			},
		},
		{
			name: "windows host",
			input: url.Values{
				"path": {`D:\Research\Project A`}, "limit": {"200"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := runtimeFileBrowseQuery(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.input) {
				t.Fatalf("query = %#v, want %#v", got, test.input)
			}
		})
	}
}

func TestRuntimeFileBrowseQueryRejectsUnboundedOrAmbiguousInput(t *testing.T) {
	for _, input := range []url.Values{
		{"limit": {"501"}},
		{"offset": {"10001"}},
		{"include_hidden": {"yes-please"}},
		{"path": {"/one", "/two"}},
		{"unexpected": {"value"}},
	} {
		if _, err := runtimeFileBrowseQuery(input); err == nil {
			t.Fatalf("invalid query unexpectedly succeeded: %#v", input)
		}
	}
}
