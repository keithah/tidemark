package hls

import "testing"

func TestBoundedMapEvictsOldestKey(t *testing.T) {
	cache := newBoundedMap[string, string](2)
	cache.Remember("a", "1")
	cache.Remember("b", "2")
	cache.Remember("c", "3")

	if _, ok := cache.Get("a"); ok {
		t.Fatal("oldest key was not evicted")
	}
	if got, ok := cache.Get("b"); !ok || got != "2" {
		t.Fatalf("b = %q, %v; want 2, true", got, ok)
	}
	if got, ok := cache.Get("c"); !ok || got != "3" {
		t.Fatalf("c = %q, %v; want 3, true", got, ok)
	}
}
