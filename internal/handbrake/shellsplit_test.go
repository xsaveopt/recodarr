package handbrake

import (
	"reflect"
	"testing"
)

func TestSplitShellArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"--two-pass --turbo", []string{"--two-pass", "--turbo"}},
		{`--audio-name "5.1 Surround"`, []string{"--audio-name", "5.1 Surround"}},
		{`--audio-name '5.1 Surround'`, []string{"--audio-name", "5.1 Surround"}},
		{`--encopts aq-mode=3:psy-rd=1.5`, []string{"--encopts", "aq-mode=3:psy-rd=1.5"}},
		{`--name "with \"quotes\" inside"`, []string{"--name", `with "quotes" inside`}},
		{`--path /media/with\ space/file.mkv`, []string{"--path", "/media/with space/file.mkv"}},
		{`a  b   c`, []string{"a", "b", "c"}},
		{`--empty ""`, []string{"--empty", ""}},
		{`--unterminated "still emitted`, []string{"--unterminated", "still emitted"}},
	}
	for _, tc := range cases {
		got := splitShellArgs(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitShellArgs(%q): got %#v, want %#v", tc.in, got, tc.want)
		}
	}
}
