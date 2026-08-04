package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedact(t *testing.T) {
	input := "peer=192.168.1.4 token=abc /var/lib/astar/chains 12D3KooWAbCdEfGhijkmnpqrstuvwxyz123456789"
	got := Redact(input)
	if got == input || regexp.MustCompile(`192\.168|token=abc|/var/lib|12D3KooW`).MatchString(got) {
		t.Fatal("input was not redacted")
	}
}
func TestDiagnoseUsesNonStoredStructuredRequest(t *testing.T) {
	client := New("test-key", "gemini-test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("x-goog-api-key") != "test-key" {
			t.Fatal("missing API key header")
		}
		raw, _ := io.ReadAll(request.Body)
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["store"] != false || payload["response_format"] == nil {
			t.Fatalf("unsafe payload: %#v", payload)
		}
		body := `{"outputs":[{"text":"{\"severity\":\"warning\",\"diagnosis\":\"確認が必要です\",\"evidence_ids\":[\"c1\"],\"recommended_action\":\"observe\",\"confidence\":0.8,\"operator_steps\":[\"監視を継続\"]}"}]}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	diagnosis, err := client.Diagnose(context.Background(), model.Overview{}, []model.LogLine{{Cursor: "c1", Message: "peer 192.168.2.1"}}, model.Incident{ID: "i1", Title: "test"})
	if err != nil || diagnosis.RecommendedAction != "observe" {
		t.Fatalf("diagnosis: %#v %v", diagnosis, err)
	}
}
func TestExtractText(t *testing.T) {
	got := extractText([]byte(`{"outputs":[{"text":"{\"severity\":\"healthy\"}"}]}`))
	if got == "" {
		t.Fatal("missing output")
	}
}
