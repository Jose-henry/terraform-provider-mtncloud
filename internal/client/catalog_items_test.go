package client

import (
	"net/http"
	"testing"
)

// A renamed item is still found by its code; a name it had finds it too; without a code list
// (refused), the customer view is matched by name.
func TestFindCatalogItem(t *testing.T) {
	ref := CatalogItemRef{Code: "MVPNSV02", Names: []string{"MTN PfSense VPN", "MTN VPN Service"}}
	t.Run("by code after a rename", func(t *testing.T) {
		f := newFakeAPI(t)
		f.route("GET", "/api/catalog-item-types", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"catalogItemTypes":[{"id":9,"code":"OTHER","name":"MTN VPN Service"},` +
				`{"id":53,"code":"mvpnsv02","name":"MTN Site VPN"}],"meta":{"total":2}}`))
		})
		it, err := f.client(t).FindCatalogItem(t.Context(), ref)
		if err != nil || it.ID != 53 || it.Name != "MTN Site VPN" {
			t.Fatalf("got %+v, %v; want item 53 by its code, before a former name", it, err)
		}
	})
	t.Run("refused code list", func(t *testing.T) {
		f := newFakeAPI(t)
		f.route("GET", "/api/catalog-item-types", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		f.route("GET", "/api/catalog/types", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"catalogItemTypes":[{"id":53,"name":"MTN VPN Service"}],"meta":{"total":1}}`))
		})
		it, err := f.client(t).FindCatalogItem(t.Context(), ref)
		if err != nil || it.ID != 53 {
			t.Fatalf("got %+v, %v", it, err)
		}
	})
	t.Run("left out of the code list", func(t *testing.T) {
		f := newFakeAPI(t)
		f.route("GET", "/api/catalog-item-types", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"catalogItemTypes":[{"id":9,"code":"OTHER","name":"Other"}],"meta":{"total":1}}`))
		})
		f.route("GET", "/api/catalog/types", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"catalogItemTypes":[{"id":53,"name":"MTN PfSense VPN"}],"meta":{"total":1}}`))
		})
		it, err := f.client(t).FindCatalogItem(t.Context(), ref)
		if err != nil || it.ID != 53 {
			t.Fatalf("got %+v, %v; want item 53 from the customer catalog, by name", it, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		f := newFakeAPI(t)
		f.route("GET", "/api/catalog-item-types", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"catalogItemTypes":[],"meta":{"total":0}}`))
		})
		if _, err := f.client(t).FindCatalogItem(t.Context(), ref); !IsNoMatch(err) {
			t.Fatalf("got %v", err)
		}
	})
}
