package message

import "testing"

func TestValidInboxLocalPartMatchesIngressContract(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "a", want: true},
		{value: "valid.name_1-test", want: true},
		{value: ""},
		{value: ".leading"},
		{value: "trailing-"},
		{value: "double..dot"},
		{value: "plus+tag"},
	} {
		if got := ValidInboxLocalPart(test.value); got != test.want {
			t.Errorf("ValidInboxLocalPart(%q) = %t, want %t", test.value, got, test.want)
		}
	}
}
