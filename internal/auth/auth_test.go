package auth

import (
	"testing"

	"druckwunsch/internal/store"
)

func TestRoleFor(t *testing.T) {
	a := &Auth{cfg: Config{PrinterGroup: "druckwunsch_full", UserGroup: "druckwunsch_limited"}}
	cases := []struct {
		claim any
		want  string
	}{
		{[]any{"family", "druckwunsch_limited"}, store.RoleRequester},
		{[]any{"druckwunsch_limited", "Druckwunsch_Full"}, store.RolePrinter},
		{"druckwunsch_full", store.RolePrinter},
		{[]any{"other"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := a.roleFor(stringList(c.claim)); got != c.want {
			t.Errorf("%v: got %q, want %q", c.claim, got, c.want)
		}
	}
	// an empty group name never matches
	if (&Auth{}).roleFor([]string{""}) != "" {
		t.Error("empty group matched")
	}
}
