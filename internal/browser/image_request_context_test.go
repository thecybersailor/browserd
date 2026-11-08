package browser

import (
	"context"
	"testing"
)

func TestSnapshotIncludesObservedImageRequestContextForCurrentPage(t *testing.T) {
	service := NewService(nil, nil, nil)
	service.captureSnapshot = func(context.Context) (snapshotRuntimeEnvelope, error) {
		return snapshotRuntimeEnvelope{SnapshotID: "snap_test", Page: PageSnapshot{URL: "https://shop.example/collections/new", Groups: map[string]PageTable{}}}, nil
	}
	service.recordObservedImageRequest("rt_1", observedImageRequest{
		URL:            "https://cdn.example/image.webp",
		PageURL:        "https://shop.example/collections/sale",
		RefererKnown:   true,
		RefererPresent: true,
		Referer:        "https://shop.example/collections/sale",
		UserAgent:      "Mozilla/5.0 sale",
	})
	service.recordObservedImageRequest("rt_1", observedImageRequest{
		URL:            "https://cdn.example/image.webp",
		PageURL:        "https://shop.example/collections/new",
		RefererKnown:   true,
		RefererPresent: true,
		Referer:        "https://shop.example/collections/new",
		UserAgent:      "Mozilla/5.0 new",
	})

	out, err := service.snapshotWithContext(context.Background(), "rt_1")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(out.Page.ImageRequests) != 1 {
		t.Fatalf("image requests = %#v", out.Page.ImageRequests)
	}
	request := out.Page.ImageRequests[0]
	if request.URL != "https://cdn.example/image.webp" || request.PageURL != "https://shop.example/collections/new" || request.Referer != "https://shop.example/collections/new" || request.UserAgent != "Mozilla/5.0 new" {
		t.Fatalf("request = %#v", request)
	}
}

func TestSnapshotDistinguishesObservedEmptyRefererFromUnknownImageRequest(t *testing.T) {
	service := NewService(nil, nil, nil)
	service.captureSnapshot = func(context.Context) (snapshotRuntimeEnvelope, error) {
		return snapshotRuntimeEnvelope{SnapshotID: "snap_test", Page: PageSnapshot{URL: "https://shop.example/collections/new", Groups: map[string]PageTable{}}}, nil
	}
	service.recordObservedImageRequest("rt_1", observedImageRequest{
		URL:            "https://cdn.example/no-referer.webp",
		PageURL:        "https://shop.example/collections/new",
		RefererKnown:   true,
		RefererPresent: false,
		UserAgent:      "Mozilla/5.0",
	})

	out, err := service.snapshotWithContext(context.Background(), "rt_1")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(out.Page.ImageRequests) != 1 || !out.Page.ImageRequests[0].RefererKnown || out.Page.ImageRequests[0].RefererPresent {
		t.Fatalf("image requests = %#v", out.Page.ImageRequests)
	}
}
