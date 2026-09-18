package mailru

import "testing"

func TestNormalizeWeblink(t *testing.T) {
	tests := map[string]string{
		"AbCdEfGh1/IjKlMnOp2":                              "AbCdEfGh1/IjKlMnOp2",
		"/AbCdEfGh1/IjKlMnOp2/":                            "AbCdEfGh1/IjKlMnOp2",
		"https://cloud.mail.ru/public/AbCdEfGh1/IjKlMnOp2": "AbCdEfGh1/IjKlMnOp2",
		"http://cloud.mail.ru/public/AbCdEfGh1/IjKlMnOp2/": "AbCdEfGh1/IjKlMnOp2",
	}
	for input, want := range tests {
		if got := NormalizeWeblink(input); got != want {
			t.Fatalf("NormalizeWeblink(%q) = %q, want %q", input, got, want)
		}
	}
}
