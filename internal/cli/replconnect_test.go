package cli

import "testing"

func TestIsConnectCmd(t *testing.T) {
	cases := []struct {
		in    string
		want  connectArgs
		err   bool
		match bool
	}{
		{in: "/build foo", match: false},
		{in: "/connect", match: true, err: true},
		{in: "/connect bogus /p", match: true, err: true},
		{in: "/connect local", match: true, err: true},
		{in: "/connect local /tmp/notes --name notes", match: true, want: connectArgs{Type: "local", Path: "/tmp/notes", Name: "notes"}},
		{in: "/connect obsidian ~/vault", match: true, want: connectArgs{Type: "obsidian", Path: "~/vault"}},
		{in: "/connect gdrive --folder abc123 --name gd", match: true, want: connectArgs{Type: "gdrive", FolderID: "abc123", Name: "gd"}},
		{in: "/connect gdrive abc123", match: true, want: connectArgs{Type: "gdrive", FolderID: "abc123"}},
		{in: "/connect confluence --space ENG --site acme", match: true, want: connectArgs{Type: "confluence", SpaceKey: "ENG", Site: "acme"}},
		{in: "/connect confluence ENG", match: true, want: connectArgs{Type: "confluence", SpaceKey: "ENG"}},
	}
	for _, tc := range cases {
		got, ok, err := isConnectCmd(tc.in)
		if ok != tc.match {
			t.Errorf("%q: match=%v want %v", tc.in, ok, tc.match)
			continue
		}
		if !tc.match {
			continue
		}
		if tc.err {
			if err == nil {
				t.Errorf("%q: want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %+v want %+v", tc.in, got, tc.want)
		}
	}
}
