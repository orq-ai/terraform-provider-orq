package client

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// formatTimestamp renders a protobuf timestamp as an RFC 3339 (UTC) string, or
// "" when the timestamp is nil. Resources surface server-set create/update
// times as computed strings, so a stable, comparable format matters.
func formatTimestamp(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339)
}

// parseTimestamp converts an RFC 3339 string into a protobuf timestamp. An empty
// string yields a nil timestamp (field omitted). A parse failure is returned so
// the caller can surface a clear validation error rather than sending garbage.
func parseTimestamp(s string) (*timestamppb.Timestamp, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return timestamppb.New(t), nil
}
