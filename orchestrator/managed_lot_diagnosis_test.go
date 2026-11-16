package orchestrator

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

type managedLotDiagnosticTransport func(*http.Request) (*http.Response, error)

func (transport managedLotDiagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestManagedLotDiagnosisIsAuthorizedAndReadOnly(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.state.LotCredentials = map[string]parser.Account{
		"player": {Login: "player", OAuth: "fixture-oauth", UserID: "17", DeviceID: "fixture-device"},
	}
	token := strings.Repeat("x", 32)
	t.Setenv("ORCHESTRATOR_LOT_PREVIEW_TOKEN", token)
	const ref = "000000000000000000000000"
	const bodyPrefix = `{"Operation":"diagnose","Lot":"` + ref + `"`
	const sample = `,"Sample":[{"Login":"player"}]}`

	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	var calls int
	var reject bool
	http.DefaultTransport = managedLotDiagnosticTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "OAuth fixture-oauth" || r.Method != http.MethodPost {
			t.Error("diagnostic did not use the selected account")
		}
		if reject {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("fixture-oauth"))}, nil
		}
		response := `{"data":{"currentUser":{"id":"17","login":"player","inventory":{"gameEventDrops":[],"dropCampaignsInProgress":[]}}}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response))}, nil
	})

	for _, tc := range []struct {
		name, body, state string
		status            int
		auth, reject      bool
		calls             int
	}{
		{"hidden from unauthorized clients", bodyPrefix + sample, "", http.StatusNotFound, false, false, 0},
		{"orchestrator only", bodyPrefix + "}", "orchestrator_ok", http.StatusOK, true, false, 0},
		{"empty sample", bodyPrefix + `,"Sample":[]}`, "twitch_no_account", http.StatusOK, true, false, 0},
		{"unavailable sample", bodyPrefix + `,"Sample":[{"Login":"missing"}]}`, "twitch_no_account", http.StatusOK, true, false, 0},
		{"reject account line", bodyPrefix + `,"AccountLine":"private-fixture"}`, "", http.StatusBadRequest, true, false, 0},
		{"reject multiple samples", bodyPrefix + `,"Sample":[{"Login":"player"},{"Login":"other"}]}`, "", http.StatusBadRequest, true, false, 0},
		{"Twitch success", bodyPrefix + sample, "twitch_ok", http.StatusOK, true, false, 1},
		{"Twitch failure redacted", bodyPrefix + sample, "twitch_unavailable", http.StatusOK, true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reject = tc.reject
			calls = 0
			r := httptest.NewRequest(http.MethodPost, "/internal/managed-lot", strings.NewReader(tc.body))
			if tc.auth {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			s.serveManagedLot(w, r)
			if w.Code != tc.status || calls != tc.calls {
				t.Fatalf("HTTP status=%d, Twitch requests=%d", w.Code, calls)
			}
			if tc.state != "" {
				var reply map[string]string
				if err := json.NewDecoder(w.Body).Decode(&reply); err != nil || len(reply) != 1 || reply["state"] != tc.state {
					t.Fatalf("unexpected diagnostic response: %v (%v)", reply, err)
				}
			}
			if len(s.state.LotCredentials) != 1 || len(s.state.LotAccounts) != 0 || len(s.state.ManagedLots) != 0 || len(s.state.LotTitles) != 0 {
				t.Fatal("diagnostic mutated lot state")
			}
		})
	}
}
