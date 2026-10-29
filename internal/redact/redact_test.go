package redact

import (
	"strings"
	"testing"
)

func TestSecretsAreHiddenButTheTextAroundThemStays(t *testing.T) {
	in := "Found it: -----BEGIN RSA PRIVATE KEY-----\nMIIEow\nabc\n-----END RSA PRIVATE KEY----- and ghp_abcdefghijklmnopqrstuvwxyz0123, " +
		"napi_g1dfwb6db5yaoi0fp7km8kfwetk46mfy, sk-ant-api03-abcdefghijklmnop, Authorization: Bearer abcdefghijklmnopqrstuvwxyz, password=hunter2hunter2"
	out := Secrets(in)
	for _, leaked := range []string{"MIIEow", "ghp_abcdef", "napi_g1df", "sk-ant-api03", "abcdefghijklmnopqrstuvwxyz", "hunter2"} {
		if strings.Contains(out, leaked) {
			t.Errorf("%q leaked: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "Found it:") || !strings.Contains(out, "password=[REDACTED]") {
		t.Errorf("surrounding text must stay: %s", out)
	}
}

func TestOrdinaryCodeIsLeftAlone(t *testing.T) {
	code := "func Reserve(token string) error { return nil } // the token is checked upstream"
	if Secrets(code) != code {
		t.Fatalf("got %s", Secrets(code))
	}
}
