package main

import "testing"

func TestAccessBase(t *testing.T) {
	cases := []struct{ addr, published, want string }{
		{"127.0.0.1:8799", "", "http://127.0.0.1:8799"},
		{"127.0.0.1:9000", "8799", "http://127.0.0.1:9000"}, // an explicit bind keeps its port
		{"localhost:8799", "", "http://localhost:8799"},
		{"[::1]:8799", "", "http://[::1]:8799"},
		{"0.0.0.0:8799", "", "http://localhost:8799"},
		{":8799", "", "http://localhost:8799"},
		{"[::]:8799", "", "http://localhost:8799"},
		{"0.0.0.0:8799", "9000", "http://localhost:9000"}, // in the container: the published port
		{"0.0.0.0:8799", "nope", "http://localhost:8799"},
		{"0.0.0.0:8799", "70000", "http://localhost:8799"},
		{"192.168.1.20:8799", "", "http://192.168.1.20:8799"},
	}
	for _, c := range cases {
		if got := accessBase(c.addr, c.published); got != c.want {
			t.Errorf("accessBase(%q, %q) = %q, want %q", c.addr, c.published, got, c.want)
		}
	}
}
