package dispatcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// goUsageBody is the shape the route returned when it was read on 2026-11-09:
// three windows, each with the vendor's status, the share used, and an ISO
// reset timestamp.
const goUsageBody = `{"usage":{
	"rolling":{"status":"ok","percent":42,"resetsAt":"2026-11-09T12:00:00Z"},
	"weekly":{"status":"rate-limited","percent":100,"resetsAt":"2026-11-10T00:00:00Z"},
	"monthly":{"status":"ok","percent":12,"resetsAt":"2026-12-01T00:00:00Z"}
}}`

// goUsageServer answers every read with body, recording the request it saw.
func goUsageServer(t *testing.T, body string, seen *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Clone()
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGoUsageClientReadsThePlansWindowsWithTheRunnersKey(t *testing.T) {
	var seen http.Header
	srv := goUsageServer(t, goUsageBody, &seen)
	client := GoUsageClient{
		Token:  func(context.Context) (string, error) { return "go-key", nil },
		URL:    srv.URL,
		Client: srv.Client(),
	}
	usage, err := client.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []GoUsageWindow{
		{Name: "5h", Status: goUsageOKStatus, Percent: 42, ResetsAt: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)},
		{Name: "week", Status: GoUsageRateLimited, Percent: 100, ResetsAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)},
		{Name: "month", Status: goUsageOKStatus, Percent: 12, ResetsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
	}
	if len(usage.Windows) != len(want) {
		t.Fatalf("windows = %+v, want %d", usage.Windows, len(want))
	}
	for i := range want {
		if usage.Windows[i] != want[i] {
			t.Errorf("window %d = %+v, want %+v", i, usage.Windows[i], want[i])
		}
	}
	if got := seen.Get("authorization"); got != "Bearer go-key" {
		t.Errorf("authorization = %q, want the runner's key as a bearer token", got)
	}
	if got := seen.Get("x-opencode-session"); got != goUsageSession {
		t.Errorf("x-opencode-session = %q, want %q", got, goUsageSession)
	}
	if got := seen.Get("accept"); got != "application/json" {
		t.Errorf("accept = %q, want application/json", got)
	}
}

// A window the route adds is ignored: the three the plan meters are what
// admission acts on.
func TestGoUsageClientIgnoresAWindowTheRouteAdds(t *testing.T) {
	srv := goUsageServer(t, `{"usage":{
		"rolling":{"status":"ok","percent":42,"resetsAt":"2026-11-09T12:00:00Z"},
		"weekly":{"status":"ok","percent":30,"resetsAt":"2026-11-10T00:00:00Z"},
		"monthly":{"status":"ok","percent":12,"resetsAt":"2026-12-01T00:00:00Z"},
		"daily":{"status":"rate-limited","percent":100,"resetsAt":"2026-11-09T10:00:00Z"}
	}}`, nil)
	client := GoUsageClient{
		Token:  func(context.Context) (string, error) { return "go-key", nil },
		URL:    srv.URL,
		Client: srv.Client(),
	}
	usage, err := client.Read(context.Background())
	if err != nil {
		t.Fatalf("Read rejected a payload carrying an extra window: %v", err)
	}
	if len(usage.Windows) != 3 {
		t.Fatalf("windows = %+v, want the plan's three", usage.Windows)
	}
}

// A read that comes back in any shape admission cannot act on is a failure, so
// the plan's fixed limits decide rather than a window being silently dropped.
func TestGoUsageClientRefusesAShapeItCannotActOn(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{"not JSON", `{"usage":`},
		{"no usage object", `{}`},
		{"no weekly window", `{"usage":{
			"rolling":{"status":"ok","percent":42,"resetsAt":"2026-11-09T12:00:00Z"},
			"monthly":{"status":"ok","percent":12,"resetsAt":"2026-12-01T00:00:00Z"}}}`},
		{"a status the route has not carried", `{"usage":{
			"rolling":{"status":"paused","percent":42,"resetsAt":"2026-11-09T12:00:00Z"},
			"weekly":{"status":"ok","percent":30,"resetsAt":"2026-11-10T00:00:00Z"},
			"monthly":{"status":"ok","percent":12,"resetsAt":"2026-12-01T00:00:00Z"}}}`},
		{"a percent above 100", `{"usage":{
			"rolling":{"status":"ok","percent":101,"resetsAt":"2026-11-09T12:00:00Z"},
			"weekly":{"status":"ok","percent":30,"resetsAt":"2026-11-10T00:00:00Z"},
			"monthly":{"status":"ok","percent":12,"resetsAt":"2026-12-01T00:00:00Z"}}}`},
		{"a percent below 0", `{"usage":{
			"rolling":{"status":"ok","percent":-1,"resetsAt":"2026-11-09T12:00:00Z"},
			"weekly":{"status":"ok","percent":30,"resetsAt":"2026-11-10T00:00:00Z"},
			"monthly":{"status":"ok","percent":12,"resetsAt":"2026-12-01T00:00:00Z"}}}`},
		{"a percent the route sent as null", `{"usage":{
			"rolling":{"status":"ok","percent":42,"resetsAt":"2026-11-09T12:00:00Z"},
			"weekly":{"status":"ok","percent":null,"resetsAt":"2026-11-10T00:00:00Z"},
			"monthly":{"status":"ok","percent":12,"resetsAt":"2026-12-01T00:00:00Z"}}}`},
		{"a reset that is not a timestamp", `{"usage":{
			"rolling":{"status":"ok","percent":42,"resetsAt":"soon"},
			"weekly":{"status":"ok","percent":30,"resetsAt":"2026-11-10T00:00:00Z"},
			"monthly":{"status":"ok","percent":12,"resetsAt":"2026-12-01T00:00:00Z"}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := goUsageServer(t, tt.body, nil)
			client := GoUsageClient{
				Token:  func(context.Context) (string, error) { return "go-key", nil },
				URL:    srv.URL,
				Client: srv.Client(),
			}
			if _, err := client.Read(context.Background()); err == nil {
				t.Fatal("Read accepted a shape admission cannot act on")
			}
		})
	}
}

func TestGoUsageClientAnswersANon200AsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	client := GoUsageClient{
		Token:  func(context.Context) (string, error) { return "stale", nil },
		URL:    srv.URL,
		Client: srv.Client(),
	}
	if _, err := client.Read(context.Background()); err == nil {
		t.Fatal("Read accepted a 401")
	}
}

// An unreadable key fails the read rather than sending an unauthenticated
// request, and a key that is empty is the same failure.
func TestGoUsageClientFailsWithoutAKey(t *testing.T) {
	srv := goUsageServer(t, goUsageBody, nil)
	for _, tt := range []struct {
		name  string
		token func(context.Context) (string, error)
	}{
		{"a key that cannot be read", func(context.Context) (string, error) { return "", errors.New("secret manager is unreachable") }},
		{"an empty key", func(context.Context) (string, error) { return "", nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := GoUsageClient{Token: tt.token, URL: srv.URL, Client: srv.Client()}
			if _, err := client.Read(context.Background()); err == nil {
				t.Fatal("Read sent a request without a key")
			}
		})
	}
}

// The read is bounded below the poll's own request timeout, so a hung route
// costs one deadline rather than the poll.
func TestGoUsageClientBoundsTheRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, goUsageBody)
	}))
	defer srv.Close()
	client := GoUsageClient{
		Token:   func(context.Context) (string, error) { return "go-key", nil },
		URL:     srv.URL,
		Client:  srv.Client(),
		Timeout: 20 * time.Millisecond,
	}
	if _, err := client.Read(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the read's own deadline", err)
	}
}

func TestGoUsageSpentBinding(t *testing.T) {
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	ok := func(name string, percent float64, resets time.Time) GoUsageWindow {
		return GoUsageWindow{Name: name, Status: goUsageOKStatus, Percent: percent, ResetsAt: resets}
	}
	limited := func(name string, resets time.Time) GoUsageWindow {
		return GoUsageWindow{Name: name, Status: GoUsageRateLimited, Percent: 100, ResetsAt: resets}
	}
	for _, tt := range []struct {
		name    string
		windows []GoUsageWindow
		want    string
	}{
		{
			"a rate-limited window",
			[]GoUsageWindow{ok("5h", 10, at.Add(time.Hour)), limited("week", at.Add(3*time.Hour)), ok("month", 60, at.Add(30*24*time.Hour))},
			"go-usage week resets " + at.Add(3*time.Hour).Format(time.RFC3339),
		},
		{
			"a window at the threshold",
			[]GoUsageWindow{ok("5h", GoUsageHeldPercent, at.Add(time.Hour))},
			"go-usage 5h resets " + at.Add(time.Hour).Format(time.RFC3339),
		},
		{
			"every window below the threshold",
			[]GoUsageWindow{ok("5h", GoUsageHeldPercent-0.1, at.Add(time.Hour)), ok("week", 40, at.Add(3*time.Hour))},
			"",
		},
		{
			"a window whose reset has passed",
			[]GoUsageWindow{limited("week", at.Add(-time.Minute))},
			"",
		},
		{
			"a window resetting exactly now",
			[]GoUsageWindow{limited("week", at)},
			"",
		},
		{
			"the window that frees last",
			[]GoUsageWindow{limited("5h", at.Add(time.Hour)), limited("week", at.Add(2*time.Hour))},
			"go-usage week resets " + at.Add(2*time.Hour).Format(time.RFC3339),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := (GoUsage{Windows: tt.windows}).SpentBinding(at); got != tt.want {
				t.Fatalf("SpentBinding = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHeldBeforeBudgetNamesTheSpentGoWindow(t *testing.T) {
	binding := "go-usage week resets 2026-11-10T00:00:00Z"
	in := AdmitInput{Facts: Facts{Limits: DefaultLimits, GoUsageHeld: binding}, N: 5}
	if got := HeldBeforeBudget(in); got != binding {
		t.Fatalf("held by %q, want %q", got, binding)
	}
	// A run inside its model's peak window as well is named by the plan's own
	// window: the live limit is decided before the pricing one.
	in.Facts.PeakHeld = true
	if got := HeldBeforeBudget(in); got != binding {
		t.Fatalf("held by %q, want the spent Go window ahead of the peak window", got)
	}
	// A halted provider is decided first: a run cannot start on a plan that is
	// refusing calls whatever its windows show.
	in.Facts.ProviderHalted = true
	if got := HeldBeforeBudget(in); got != CeilingProviderHalted {
		t.Fatalf("held by %q, want the provider halt first", got)
	}
}

func TestHoldReasonCountsAGoUsageHoldAsABudget(t *testing.T) {
	if got := holdReason(CeilingGoUsage+" week resets 2026-11-10T00:00:00Z", BudgetConfig{}); got != HoldBudget {
		t.Fatalf("holdReason = %q, want %q", got, HoldBudget)
	}
}

// The plan the route reports on must stay a configured plan: a rename in the
// provider configuration would otherwise silence the read, and admission would
// quietly go back to the fixed limits alone.
func TestGoUsagePlanIsAConfiguredPlan(t *testing.T) {
	if !slices.Contains(providers.PaidPlans(), GoUsagePlan) {
		t.Fatalf("%q is not a configured paid plan, so its live usage would never be read", GoUsagePlan)
	}
}
