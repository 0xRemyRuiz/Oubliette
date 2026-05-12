package vm

import (
	"errors"
	"testing"
)

func TestParseFirstIPv4(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{
			name: "typical single interface",
			input: " Name       MAC address          Protocol  Address\n" +
				" -------------------------------------------------------\n" +
				" vnet0      52:54:00:ab:cd:ef    ipv4      192.168.122.10/24\n",
			want: "192.168.122.10",
		},
		{
			name: "multiple interfaces picks first ipv4",
			input: " Name       MAC address          Protocol  Address\n" +
				" -------------------------------------------------------\n" +
				" vnet0      52:54:00:ab:cd:ef    ipv6      fe80::5054:ff:feab:cdef/64\n" +
				" vnet1      52:54:00:11:22:33    ipv4      10.0.0.5/8\n",
			want: "10.0.0.5",
		},
		{
			name: "ipv6 only",
			input: " Name       MAC address          Protocol  Address\n" +
				" -------------------------------------------------------\n" +
				" vnet0      52:54:00:ab:cd:ef    ipv6      fe80::1/64\n",
			wantErr: ErrNoIPv4,
		},
		{
			name:    "empty output",
			input:   "",
			wantErr: ErrNoIPv4,
		},
		{
			name:    "header only",
			input:   " Name       MAC address          Protocol  Address\n --------\n",
			wantErr: ErrNoIPv4,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFirstIPv4(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error: got %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
