package hosts

import (
	"context"
	"testing"

	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestRefreshSeesOtherWriters: a registry picks up hosts and webhooks that
// another process (a replica, "console hosts import") wrote (issue #101).
func TestRefreshSeesOtherWriters(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	box := testBox(t)
	a := New(fileConfig(t), st, box, nil)
	if _, err := a.Init(ctx); err != nil {
		t.Fatal(err)
	}
	b := New(fileConfig(t), st, box, nil) // the running console
	if _, err := b.Init(ctx); err != nil {
		t.Fatal(err)
	}
	changes := 0
	b.OnChange = func() { changes++ }
	if changed, err := b.Refresh(ctx); err != nil || changed {
		t.Fatalf("nothing changed yet: %v %v", changed, err)
	}

	gl, _ := a.Find(GitLab, "gitlab.example.com")
	e, _, err := a.AddEndpoint(ctx, gl.ID, "imported", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Route(GitLab, e.ID); err == nil {
		t.Fatal("not seen before a refresh (the bug)")
	}
	if changed, err := b.Refresh(ctx); err != nil || !changed || changes != 1 {
		t.Fatalf("refresh after another writer: %v %v (OnChange %d)", changed, err, changes)
	}
	if _, _, err := b.Route(GitLab, e.ID); err != nil {
		t.Errorf("the new webhook routes after a refresh: %v", err)
	}
	if changed, _ := b.Refresh(ctx); changed {
		t.Error("a second refresh without changes does nothing")
	}

	// Its own writes don't make it reload again.
	if err := b.DeleteEndpoint(ctx, e.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	if changed, _ := b.Refresh(ctx); changed {
		t.Error("a registry's own write is already loaded")
	}
	if changed, _ := a.Refresh(ctx); !changed {
		t.Error("the other registry sees the delete")
	}
	if _, _, err := a.Route(GitLab, e.ID); err == nil {
		t.Error("a deleted webhook stops routing")
	}
}
