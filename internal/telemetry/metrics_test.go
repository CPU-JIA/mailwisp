package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsExposeBoundedApplicationSignals(t *testing.T) {
	metrics := NewMetrics(nil)
	metrics.ObserveHTTPRequest(http.MethodGet, "GET /api/v1/inboxes/me/messages/{id}", http.StatusOK, 25*time.Millisecond)
	metrics.LMTPSessionOpened()
	metrics.LMTPSessionClosed()
	metrics.LMTPSessionRejected()
	metrics.ObserveLMTPDelivery(451)
	metrics.ObserveLMTPQuotaRejected("storage_bytes")
	metrics.ObserveLMTPQuotaRejected("unbounded")
	metrics.ObserveLMTPStorageRejected("capacity")
	metrics.ObserveLMTPStorageRejected("unbounded")
	metrics.ObserveParser("success", 10*time.Millisecond)
	metrics.ObserveRetention("success", 2, 1)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`mailwisp_http_requests_total{method="GET",route="GET /api/v1/inboxes/me/messages/{id}",status="200"} 1`,
		`mailwisp_lmtp_sessions_rejected_total 1`,
		`mailwisp_lmtp_deliveries_total{result="temporary_failure"} 1`,
		`mailwisp_lmtp_quota_rejections_total{reason="storage_bytes"} 1`,
		`mailwisp_lmtp_storage_rejections_total{reason="capacity"} 1`,
		`mailwisp_parser_runs_total{result="success"} 1`,
		`mailwisp_retention_deleted_total{kind="inbox"} 2`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics output missing %q", expected)
		}
	}
}
