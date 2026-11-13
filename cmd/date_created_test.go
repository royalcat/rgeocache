package main

import (
	"testing"
	"time"
)

func TestParseDateCreated(t *testing.T) {
	flagTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	tests := []struct {
		name, flag, epoch string
		want              time.Time
		wantErr           bool
	}{
		{name: "none"},
		{name: "flag", flag: "2026-01-02T03:04:05Z", want: flagTime},
		{name: "epoch", epoch: "1767323045", want: time.Unix(1767323045, 0)},
		{name: "flag wins over epoch", flag: "2026-01-02T03:04:05Z", epoch: "1", want: flagTime},
		{name: "bad flag", flag: "yesterday", wantErr: true},
		{name: "bad epoch", epoch: "soon", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDateCreated(tc.flag, tc.epoch)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
