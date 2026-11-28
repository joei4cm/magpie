package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #8087: an image-only routing group is what /v1/models lists agents by —
// its bare id, no "group/" on it — and a client sends the list's id back as
// it came. Resolve leaves such a group to chat, so the draw's own names
// take the bare id too: the group is found by it and drawn on by its
// member, as "group/<id>" is.
func TestDrawOnAGroupNamedByItsBareID(t *testing.T) {
	s, e := easeled(t)
	if err := provider.SaveGroup(provider.Group{ID: "gpt-image", Name: "GPT Image", Members: []string{"art/gpt-image-1"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"gpt-image","prompt":"a magpie"}`)
	if code != 200 || len(a.Data) != 1 || a.Model != "art/gpt-image-1" {
		t.Fatalf("the bare group id didn't draw: %d %s", code, raw)
	}
	if sent := e.got("/v1/images/generations"); len(sent) != 1 || !strings.Contains(sent[0], `"model":"gpt-image-1"`) {
		t.Fatalf("the member was sent %v", sent)
	}
}

// the video route's sibling: a video-only routing group named by its bare
// id starts on its member, as "group/<id>" does.
func TestVideoOnAGroupNamedByItsBareID(t *testing.T) {
	fresh(t)
	up := newOpenAIVideos(t)
	saveFetched(t, provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "sk-studio"})
	if err := provider.SaveGroup(provider.Group{ID: "reels", Name: "Reels", Members: []string{"studio/vid-1"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	code, obj, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"reels","prompt":"a cat on a fence","seconds":4}`)
	if code != 200 || obj["model"] != "studio/vid-1" {
		t.Fatalf("the bare group id didn't start it: %d %s", code, raw)
	}
}
