package query

import (
	"context"
	"errors"
	"testing"
)

func TestBandwidthRejectsUnknownSplit(t *testing.T) {
	_, err := NewReader(nil).Bandwidth(context.Background(), BandwidthRequest{Split: "status"})
	if !errors.Is(err, ErrUnknownBandwidthSplit) {
		t.Fatalf("error %v, want ErrUnknownBandwidthSplit", err)
	}
}
