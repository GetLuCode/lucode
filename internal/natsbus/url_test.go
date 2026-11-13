package natsbus

import "testing"

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "",
		"127.0.0.1":                 "nats://127.0.0.1:4222",
		"192.168.1.5:4223":          "nats://192.168.1.5:4223",
		"hub":                       "nats://hub:4222",
		"hub.home":                  "nats://hub.home:4222",
		"hub:4333":                  "nats://hub:4333",
		"nats://hub":                "nats://hub:4222",
		"nats://hub:4222":           "nats://hub:4222",
		"nats://127.0.0.1:4222":     "nats://127.0.0.1:4222",
		"tls://hub":                 "tls://hub:4222",
		"ws://hub:8080":             "ws://hub:8080",
		"ws://hub":                  "ws://hub",
		"wss://tok@lucode.home":     "wss://tok@lucode.home",
		"::1":                       "nats://[::1]:4222",
		"[::1]":                     "nats://[::1]:4222",
		"[::1]:4333":                "nats://[::1]:4333",
		"user:pass@hub":             "nats://user:pass@hub:4222",
		"nats://user:pass@hub:4223": "nats://user:pass@hub:4223",
		"a, b:4223":                 "nats://a:4222,nats://b:4223",
		"nats://a:4222,nats://b":    "nats://a:4222,nats://b:4222",
	} {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"ws://secret@127.0.0.1:8190":        "ws://***@127.0.0.1:8190",
		"nats://u:p@hub:4222,nats://b:4222": "nats://***@hub:4222,nats://b:4222",
		"nats://hub:4222":                   "nats://hub:4222",
		"wss://lucode.home/a@b":             "wss://lucode.home/a@b",
	} {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
