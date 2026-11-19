package redact

import (
	"strings"
	"testing"
)

// #7239: a secret cut off in the middle of a tool's output went to the
// vendor as it was, though the same secret was masked before. The
// reporter's bifrost config.json holds an Azure Speech subscription key
// under providers.azure.keys[0].value: 84 characters. The read tool
// showed the field as a placeholder, and a bash command that dumped the
// same file and cut it at 200 characters (125 of them the JSON's prefix)
// left the first 75 characters of the key in the tool's output as they
// were.
func TestKnownValueCutOffIsMasked(t *testing.T) {
	o := Options{Secrets: true}
	const key = "A1aaAAaaAA1aa1aA111Aaa1aaaaA1aaA1aaAa1AAaa1a1AaaaAA1AAAA11AAAA1aAaAAA1a1AAAAAAAAAaaa"
	const cut = "A1aaAAaaAA1aa1aA111Aaa1aaaaA1aaA1aaAa1AAaa1a1AaaaAA1AAAA11AAAA1aAaAAA1a1AAA"
	const prefix = `{"$schema": "https://www.getbifrost.ai/schema", "providers": {"azure": {"keys": [{"name": "azure-speech-eastasia", "value": "`
	if len(key) != 84 || len(cut) != 75 || len(prefix) != 125 || !strings.HasPrefix(key, cut) {
		t.Fatalf("the reporter's lengths: key %d, cut %d, prefix %d", len(key), len(cut), len(prefix))
	}

	// the read turn: the key was masked (the local model took it for a
	// secret), so it is known
	if out, n := Mask("API_KEY="+key, o); n != 1 || strings.Contains(out, key) {
		t.Fatalf("the key was not masked to begin with: %q", out)
	}

	// the bash turn: the same file, cut at 200 characters
	out, n := Mask(prefix+cut, o)
	if strings.Contains(out, cut) {
		t.Fatalf("the cut key went out as it was: %q", out)
	}
	if n != 1 {
		t.Fatalf("%d masked, want the cut key's one: %q", n, out)
	}
	// and the agent reads it back as it was
	if back := Restore(out, false); back != prefix+cut {
		t.Fatalf("round trip: %q", back)
	}

	// in a request body, as a tool's output goes
	body := `{"messages":[{"role":"tool","content":"{\"providers\": {\"azure\": {\"keys\": [{\"name\": \"azure-speech-eastasia\", \"value\": \"`
	body += cut + `\"}]}}"}]}`
	got, n := MaskJSON([]byte(body), o)
	if strings.Contains(string(got), cut) {
		t.Fatalf("the cut key went out in a request body: %s", got)
	}
	if n != 1 {
		t.Fatalf("%d masked in the body, want the cut key's one: %s", n, got)
	}
}

// A cut value too short to be told from ordinary text is left as it is,
// and a value that goes on with characters of its own is another value.
func TestCutValueTooShortIsLeft(t *testing.T) {
	o := Options{Secrets: true}
	const secret = "Passw0rd123"
	if out, n := Mask("API_KEY="+secret, o); n != 1 {
		t.Fatalf("the secret was not masked to begin with: %q", out)
	}
	// eight characters of it is no secret by itself
	if out, n := Mask("the value is Itrci6xt now", o); n != 0 || out != "the value is Itrci6xt now" {
		t.Fatalf("a short cut masked: %q", out)
	}
	// and a value that goes on with characters of its own is another value
	longer := "the value is " + secret + "XYZ now"
	if out, n := Mask(longer, o); n != 0 || out != longer {
		t.Fatalf("a longer value masked: %q", out)
	}
}
