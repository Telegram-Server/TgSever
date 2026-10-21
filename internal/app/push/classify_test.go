package push

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestClassifyMapsGatewayResponses(t *testing.T) {
	cases := []struct {
		name   string
		result Result
		err    error
		want   Outcome
	}{
		{"accepted", Result{StatusCode: 200}, nil, OutcomeSent},
		{"410 unregistered", Result{StatusCode: 410, Reason: "Unregistered"}, nil, OutcomeInvalidToken},
		{"410 without reason", Result{StatusCode: 410}, nil, OutcomeInvalidToken},
		{"410 expired token", Result{StatusCode: 410, Reason: "ExpiredToken"}, nil, OutcomeInvalidToken},
		{"bad device token", Result{StatusCode: 400, Reason: "BadDeviceToken"}, nil, OutcomeInvalidToken},
		{"token not for topic", Result{StatusCode: 400, Reason: "DeviceTokenNotForTopic"}, nil, OutcomeInvalidToken},
		{"unregistered reason on other status", Result{StatusCode: 400, Reason: "Unregistered"}, nil, OutcomeInvalidToken},
		{"too many requests", Result{StatusCode: 429, Reason: "TooManyRequests"}, nil, OutcomeRetryable},
		{"internal server error", Result{StatusCode: 500, Reason: "InternalServerError"}, nil, OutcomeRetryable},
		{"service unavailable", Result{StatusCode: 503, Reason: "ServiceUnavailable"}, nil, OutcomeRetryable},
		{"shutdown", Result{StatusCode: 503, Reason: "Shutdown"}, nil, OutcomeRetryable},
		{"timeout", Result{}, context.DeadlineExceeded, OutcomeRetryable},
		{"network error", Result{}, &net.OpError{Op: "dial", Err: errors.New("connection refused")}, OutcomeRetryable},
		{"bad topic", Result{StatusCode: 400, Reason: "BadTopic"}, nil, OutcomePermanent},
		{"payload too large", Result{StatusCode: 413, Reason: "PayloadTooLarge"}, nil, OutcomePermanent},
		{"expired provider token", Result{StatusCode: 403, Reason: "ExpiredProviderToken"}, nil, OutcomePermanent},
		{"bad certificate", Result{StatusCode: 403, Reason: "BadCertificate"}, nil, OutcomePermanent},
		{"unexpected status", Result{StatusCode: 302}, nil, OutcomePermanent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.result, tc.err); got != tc.want {
				t.Fatalf("Classify(%+v, %v) = %v, want %v", tc.result, tc.err, got, tc.want)
			}
		})
	}
}
