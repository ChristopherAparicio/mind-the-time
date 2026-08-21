package config

import (
	"testing"
	"time"
)

func TestLoadUsesSafeDefaultsAndOptionalPRIM(t *testing.T) {
	config, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.Addr != "127.0.0.1:8080" || !config.CookieSecure {
		t.Errorf("config = %#v, want default address and secure cookie", config)
	}
	if got, want := config.CookieName, "mind_the_time_owner"; got != want {
		t.Errorf("CookieName = %q, want %q", got, want)
	}
	if got, want := config.StorePath, "data/mind-the-time.json"; got != want {
		t.Errorf("StorePath = %q, want %q", got, want)
	}
	if config.PRIMConfigured() {
		t.Error("PRIMConfigured() = true with no endpoint or key")
	}
	if config.HTTPTimeout != 10*time.Second || config.CacheTTL != 5*time.Minute || config.CacheMaxEntries != 128 {
		t.Errorf("config defaults = %#v", config)
	}
}

func TestLoadReadsStorePath(t *testing.T) {
	config, err := Load(func(key string) string {
		if key == "STORE_PATH" {
			return " /var/lib/mind-the-time/state.json "
		}
		return ""
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := config.StorePath, "/var/lib/mind-the-time/state.json"; got != want {
		t.Errorf("StorePath = %q, want %q", got, want)
	}
}

func TestLoadReadsCookieSecure(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "true", value: "true", want: true},
		{name: "false", value: "false", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := Load(func(key string) string {
				if key == "COOKIE_SECURE" {
					return test.value
				}
				return ""
			})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if config.CookieSecure != test.want {
				t.Errorf("CookieSecure = %t, want %t", config.CookieSecure, test.want)
			}
		})
	}
}

func TestLoadPrefersHTTPAddrForTheServerListenAddress(t *testing.T) {
	config, err := Load(func(key string) string {
		switch key {
		case "HTTP_ADDR":
			return "0.0.0.0:5103"
		case "ADDR":
			return ":8080"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := config.Addr, "0.0.0.0:5103"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
}

func TestPRIMConfiguredRequiresAValidHTTPSEndpoint(t *testing.T) {
	config := Config{PRIMEndpoint: "not-a-url", PRIMAPIKey: "key"}
	if config.PRIMConfigurationReady() {
		t.Error("PRIMConfigurationReady() = true for malformed endpoint")
	}

	config.PRIMEndpoint = "https://prim.example.test/routes"
	if !config.PRIMConfigurationReady() {
		t.Error("PRIMConfigurationReady() = false for valid HTTPS endpoint and key")
	}
	if !config.PRIMConfigured() {
		t.Error("PRIMConfigured() = false for a valid HTTPS endpoint and key")
	}
}

func TestLoadReadsDocumentedPRIMEnvironmentVariables(t *testing.T) {
	config, err := Load(func(key string) string {
		switch key {
		case "IDFM_PRIM_ROUTE_URL":
			return " https://prim.example.test/routes "
		case "IDFM_PRIM_API_KEY":
			return " api-key "
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := config.PRIMEndpoint, "https://prim.example.test/routes"; got != want {
		t.Errorf("PRIMEndpoint = %q, want %q", got, want)
	}
	if got, want := config.PRIMAPIKey, "api-key"; got != want {
		t.Errorf("PRIMAPIKey = %q, want %q", got, want)
	}
	if !config.PRIMConfigurationReady() {
		t.Error("PRIMConfigurationReady() = false for documented HTTPS URL and API key")
	}
	if !config.PRIMConfigured() {
		t.Error("PRIMConfigured() = false when PRIM URL and API key are configured")
	}
}

func TestLoadReadsStrictReplayMode(t *testing.T) {
	configuration, err := Load(func(key string) string {
		if key == "PRIM_REPLAY_STRICT" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !configuration.PRIMReplayStrict {
		t.Error("PRIMReplayStrict = false, want true")
	}
}
