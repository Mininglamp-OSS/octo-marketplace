package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
)

func TestAdminUpdateRatingValidatesAndForwardsAuditIdentity(t *testing.T) {
	plugin := &model.Plugin{ID: "plugin-1"}
	store := &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": plugin}}
	svc := New(store)
	caller := Caller{UID: "admin", Name: "Root", RequestID: "request-1"}

	for _, rating := range []*int{nil, intPointer(1), intPointer(5)} {
		got, err := svc.AdminUpdateRating(context.Background(), caller, "plugin-1", rating)
		if err != nil {
			t.Fatalf("rating %v: %v", rating, err)
		}
		if !store.ratingScope.Admin || store.ratingParams.OperatorID != "admin" || store.ratingParams.OperatorName != "Root" || store.ratingParams.RequestID != "request-1" {
			t.Fatalf("scope/attribution not forwarded: %#v %#v", store.ratingScope, store.ratingParams)
		}
		if got.Rating != rating {
			t.Fatalf("rating pointer not forwarded: got %v want %v", got.Rating, rating)
		}
	}
}

func TestAdminUpdateRatingRejectsEmbeddedChildAsNotFound(t *testing.T) {
	store := &fakeStore{plugins: map[string]*model.Plugin{
		"skill-embedded": {ID: "skill-embedded", IsEmbedded: true},
	}}
	svc := New(store)
	rating := 5

	_, err := svc.AdminUpdateRating(context.Background(), Caller{UID: "admin"}, "skill-embedded", &rating)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
	if store.ratingParams.PluginID != "" {
		t.Fatalf("embedded child reached UpdateRating: %#v", store.ratingParams)
	}
}

func TestAdminUpdateRatingRejectsOutOfRangeWithoutStoreCall(t *testing.T) {
	store := &fakeStore{plugins: map[string]*model.Plugin{"plugin-1": {ID: "plugin-1"}}}
	svc := New(store)
	for _, value := range []int{0, 6} {
		store.ratingParams = pluginrepo.RatingParams{}
		_, err := svc.AdminUpdateRating(context.Background(), Caller{UID: "admin"}, "plugin-1", &value)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("rating %d err=%v", value, err)
		}
		if store.ratingParams.PluginID != "" {
			t.Fatalf("store called for rating %d", value)
		}
	}
}

func intPointer(v int) *int { return &v }
