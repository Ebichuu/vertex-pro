package sitecredentials

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCredentialScopeAndRedirect(t *testing.T) {
	t.Setenv("VERTEX_SITE_TEST", `{"Cookie":"session=fixture-secret","Authorization":"Bearer fixture-token"}`)
	var leaked bool
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer outside.Close()
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "session=fixture-secret" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("credential not applied")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, outside.URL, 302)
		}
	}))
	defer inside.Close()
	for _, suffix := range []string{"/ok", "/redirect"} {
		req, _ := http.NewRequest("GET", inside.URL+suffix, nil)
		client, err := Apply(req, inside.Client(), "env:VERTEX_SITE_TEST", inside.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if (suffix == "/redirect") != (err != nil) {
			t.Errorf("%s err=%v", suffix, err)
		}
	}
	if leaked {
		t.Fatal("credential escaped site origin")
	}
	req, _ := http.NewRequest("GET", outside.URL, nil)
	if _, err := Apply(req, inside.Client(), "env:VERTEX_SITE_TEST", inside.URL); err == nil {
		t.Fatal("cross origin allowed")
	}
	for _, ref := range []string{"file:C:/private", "env:PATH", "env:VERTEX_SITE_TEST\r\n"} {
		if Validate(ref) == nil {
			t.Errorf("accepted %q", ref)
		}
	}
}

func TestCredentialRedirectCallbackCannotChangeOrigin(t *testing.T) {
	t.Setenv("VERTEX_SITE_TEST", `{"Cookie":"session=fixture-secret"}`)
	var leaked bool
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer outside.Close()
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/next", http.StatusFound) }))
	defer inside.Close()
	client := inside.Client()
	client.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
		changed, _ := url.Parse(outside.URL)
		next.URL = changed
		return nil
	}
	request, _ := http.NewRequest("GET", inside.URL, nil)
	guarded, err := Apply(request, client, "env:VERTEX_SITE_TEST", inside.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := guarded.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || leaked {
		t.Fatalf("unsafe redirect: err=%v leaked=%v", err, leaked)
	}
}

func TestCredentialsRejectPlainHTTPRemoteOrigin(t *testing.T) {
	t.Setenv("VERTEX_SITE_TEST", `{"Authorization":"Bearer fixture"}`)
	request, _ := http.NewRequest("GET", "http://example.test/rss", nil)
	if _, err := Apply(request, http.DefaultClient, "env:VERTEX_SITE_TEST", "http://example.test"); err == nil {
		t.Fatal("sent credentials over remote HTTP")
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("credentials were applied to rejected request")
	}
}
