package middleware

import "testing"

func TestChatCredentialOriginBinding(t *testing.T) {
	cases := []struct {
		name    string
		bound   string
		origin  string
		matches bool
	}{
		{name: "exact", bound: "https://chat.example", origin: "https://chat.example", matches: true},
		{name: "case insensitive host", bound: "https://CHAT.example", origin: "https://chat.EXAMPLE", matches: true},
		{name: "different scheme", bound: "https://chat.example", origin: "http://chat.example", matches: false},
		{name: "different port", bound: "https://chat.example", origin: "https://chat.example:8443", matches: false},
		{name: "missing origin", bound: "https://chat.example", origin: "", matches: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := chatCredentialOriginMatches(testCase.bound, testCase.origin); got != testCase.matches {
				t.Fatalf("chatCredentialOriginMatches() = %v, want %v", got, testCase.matches)
			}
		})
	}
}
