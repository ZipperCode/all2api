package logstore

import (
	"path/filepath"
	"testing"
)

func TestStoreAppendReadAndClear(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "gateway.jsonl"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store.Record(Event{Kind: "proxy", Level: "info", Platform: "football", Message: "ok"})
	store.Record(Event{Kind: "admin", Level: "warn", Action: "config_update", Message: "changed"})

	events, err := store.Read(Query{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Kind != "admin" || events[1].Kind != "proxy" {
		t.Errorf("events order = %+v, want newest first", events)
	}

	proxyEvents, err := store.Read(Query{Limit: 10, Kind: "proxy", Platform: "football"})
	if err != nil {
		t.Fatalf("Read filtered: %v", err)
	}
	if len(proxyEvents) != 1 || proxyEvents[0].Message != "ok" {
		t.Errorf("filtered events = %+v", proxyEvents)
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	events, err = store.Read(Query{Limit: 10})
	if err != nil {
		t.Fatalf("Read after clear: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events after clear = %+v, want empty", events)
	}
}
