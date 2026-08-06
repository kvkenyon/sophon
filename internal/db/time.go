package db

import "time"

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(raw string) (time.Time, error) { return time.Parse(time.RFC3339Nano, raw) }

func parseOptionalTime(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	value, err := parseTime(*raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}
