package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// A picture's data URL is written into the body as it is encoded: the
// bytes encoding/json writes for the URL as a string are the bytes the
// body carries, and the body is written without a URL of its own made
// first (#6316).
func TestPictureBase64IsJSONMarshalOfTheURL(t *testing.T) {
	pictures := []picture{
		{Mime: "image/png", Data: []byte{0, 1, 2, 3}},
		{Mime: "image/jpeg", Data: bytes.Repeat([]byte("A"), 1000)},
		{Mime: "image/png"},
		{URL: "https://example.com/a.png?x=1&y=2"},
		{URL: "https://example.com/a.png", Data: []byte("kept by the vendor")},
	}
	for _, p := range pictures {
		url := p.URL
		if url == "" {
			url = "data:" + p.Mime + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
		}
		want, err := json.Marshal(url)
		if err != nil {
			t.Fatal(err)
		}
		v := p.dataURL()
		if got, err := json.Marshal(v); err != nil || !bytes.Equal(got, want) {
			t.Errorf("json.Marshal of %q: got %s (%v), want %s", url, got, err, want)
		}
		n, ok := jsonSize(v)
		got, err := appendJSONValue(nil, v)
		if !ok || err != nil || n != len(got) || !bytes.Equal(got, want) {
			t.Errorf("appendJSONValue of %q: %d measured, %s (%v), want %d %s", url, n, got, err, len(want), want)
		}
	}
}

// A videos body is the bytes encoding/json wrote for it: the picture's
// URL as a string and as the value are the same body, so a vendor is
// asked the same thing as before (#6316).
func TestVideoBodyKeepsItsBytes(t *testing.T) {
	pic := picture{Mime: "image/png", Data: []byte{0, 1, 2, 3}}
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pic.Data)
	f := filming{Prompt: "p", Start: &pic, References: []picture{pic}}
	got, err := grokVideoBody("m", f)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(map[string]any{
		"model":            "m",
		"prompt":           "p",
		"image":            map[string]any{"url": url},
		"reference_images": []map[string]any{{"url": url}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

// The bodies a vendor gets are the bytes they were: a picture's URL as a
// string and as the value write the same body, so a vendor is asked the
// same thing as before (#6316).
func TestGenerationBodiesKeepTheirBytes(t *testing.T) {
	pic := picture{Mime: "image/png", Data: []byte{0, 1, 2, 3}}
	url := "data:image/png;base64,AAECAw=="
	d := drawing{Prompt: "p", Images: []picture{pic}}
	f := filming{Prompt: "p", Start: &pic}
	cases := []struct {
		name string
		body func() ([]byte, error)
		want string
	}{
		{"minimax", func() ([]byte, error) { return minimaxDrawBody("m", d) },
			`{"model":"m","n":1,"prompt":"p","response_format":"base64","subject_reference":[{"image_file":"` + url + `","type":"character"}]}`},
		{"dashscope", func() ([]byte, error) { return dashScopeBody("m", d) },
			`{"input":{"messages":[{"content":[{"image":"` + url + `"},{"text":"p"}],"role":"user"}]},"model":"m","parameters":{"watermark":false}}`},
		{"grok video", func() ([]byte, error) { return grokVideoBody("m", f) },
			`{"image":{"url":"` + url + `"},"model":"m","prompt":"p"}`},
	}
	for _, c := range cases {
		got, err := c.body()
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

// A videos body with nothing in it says null, as encoding/json wrote it:
// the body's writer writes a nil list as null too, so the bytes are kept
// as they were (#6316).
func TestVideoBodyWithNoContentIsNull(t *testing.T) {
	got := volcengineVideoBody("m", filming{})
	if !strings.Contains(string(got), `"content":null`) {
		t.Fatalf("got %s", got)
	}
}

// No base64 of a picture is made first: it is encoded into the body as
// the body is written, so a 2 MiB picture costs its body and not the body
// twice (#6316).
func TestPictureBase64MakesNoBase64First(t *testing.T) {
	pic := picture{Mime: "image/png", Data: bytes.Repeat([]byte("A"), 2<<20)}
	f := filming{Prompt: "p", Start: &pic}
	var body []byte
	write := allocated(func() { body, _ = grokVideoBody("m", f) })
	if len(body) == 0 {
		t.Fatal("no body")
	}
	if limit := uint64(len(body)) * 3 / 2; write > limit {
		t.Fatalf("a %d-byte body allocated %d bytes to write, over 1.5 times its size", len(body), write)
	}
	// the measure tells the two apart: the same body the old way, with
	// the URL made first and the body written from it
	old := allocated(func() {
		url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pic.Data)
		b, _ := json.Marshal(map[string]any{"model": "m", "prompt": "p", "image": map[string]any{"url": url}})
		_ = b
	})
	if old <= write {
		t.Fatalf("the old way allocated %d bytes and this one %d: the check above shows nothing", old, write)
	}
}
