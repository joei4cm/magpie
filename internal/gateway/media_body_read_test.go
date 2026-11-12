package gateway

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// goneReader is a request's body that is gone: reading it fails.
type goneReader struct{}

func (goneReader) Read([]byte) (int, error) {
	return 0, errors.New("the request's body was read twice")
}

// #3069 (zzjcool): the images and videos handlers read the request's body a
// second time — a full copy of it — after the gateway's body room had
// already read it and bounded it. The media handler is given those bytes
// now, so its JSON branch reads them and the request's stream is left
// alone.
func TestMediaJSONReadsTheBodyItIsGiven(t *testing.T) {
	body := []byte(`{"model":"fake/m1","prompt":"tail"}`)
	for _, tc := range []struct {
		name string
		read func(*http.Request, []byte) (string, error)
	}{
		{"images", func(r *http.Request, b []byte) (string, error) {
			d, err := readDrawing(r, b)
			return d.Prompt, err
		}},
		{"videos", func(r *http.Request, b []byte) (string, error) {
			f, err := readFilming(r, b)
			return f.Prompt, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/images/edits", bytes.NewReader(body))
			r.Body = io.NopCloser(goneReader{})
			prompt, err := tc.read(r, body)
			if err != nil {
				t.Fatalf("the media body was read from the request's stream: %v", err)
			}
			if prompt != "tail" {
				t.Fatalf("prompt %q", prompt)
			}
		})
	}
}
