package client

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rayzorben/net-bond-007/internal/config"
)

// Two pages load the forwards list, then both save. The second save was made
// from a stale copy, so it must be refused (409) instead of dropping the
// forward the first one added.
func TestPutForwardsStaleVersionConflicts(t *testing.T) {
	cfg := config.DefaultClient()
	cfg.Forwards = []config.Forward{{Proto: "tcp", Port: 40001, Device: "192.168.8.193"}}
	c := New(config.NewMemory(cfg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	Backend{C: c}.Routes(mux)

	do := func(method, body, ifMatch string) (*httptest.ResponseRecorder, ForwardsView) {
		req := httptest.NewRequest(method, "/api/x/forwards", strings.NewReader(body))
		if ifMatch != "" {
			req.Header.Set("If-Match", `"`+ifMatch+`"`)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var v ForwardsView
		json.Unmarshal(rec.Body.Bytes(), &v)
		return rec, v
	}

	_, a := do("GET", "", "")
	_, b := do("GET", "", "")
	if a.Version == "" || a.Version != b.Version {
		t.Fatalf("versions %q %q", a.Version, b.Version)
	}

	// Page A adds 40002.
	rec, _ := do("PUT", `[{"proto":"tcp","port":40001,"device":"192.168.8.193"},{"proto":"both","port":40002,"device":"192.168.8.5"}]`, a.Version)
	if rec.Code != http.StatusOK {
		t.Fatalf("first save: %d %s", rec.Code, rec.Body)
	}

	// Page B, still on the old list, turns 40001 off.
	rec, _ = do("PUT", `[{"proto":"tcp","port":40001,"device":"192.168.8.193","disabled":true}]`, b.Version)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale save: got %d, want 409", rec.Code)
	}
	if got := c.cfg.Get().Forwards; len(got) != 2 || got[0].Disabled {
		t.Fatalf("stale save changed the list: %+v", got)
	}

	// After reloading, B's save goes through.
	_, b = do("GET", "", "")
	rec, _ = do("PUT", `[{"proto":"tcp","port":40001,"device":"192.168.8.193","disabled":true},{"proto":"both","port":40002,"device":"192.168.8.5"}]`, b.Version)
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh save: %d %s", rec.Code, rec.Body)
	}
	if got := c.cfg.Get().Forwards; len(got) != 2 || !got[0].Disabled {
		t.Fatalf("fresh save not applied: %+v", got)
	}
}
