package channel

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeRechargeURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty", input: "  ", want: ""},
		{name: "https", input: " https://example.com/topup?plan=pro ", want: "https://example.com/topup?plan=pro"},
		{name: "http", input: "http://example.com/recharge", want: "http://example.com/recharge"},
		{name: "relative", input: "/topup", wantErr: true},
		{name: "unsupported scheme", input: "javascript:alert(1)", wantErr: true},
		{name: "credentials", input: "https://user:password@example.com/topup", wantErr: true},
		{name: "too long", input: "https://example.com/" + strings.Repeat("a", 2048), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRechargeURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if !errors.Is(err, ErrInvalidRechargeURL) {
					t.Fatalf("error = %v, want ErrInvalidRechargeURL", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeRechargeURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
