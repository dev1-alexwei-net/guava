package executor

import (
	"net/http"
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		DatabasePath:     "/tmp/fileexplorer.sqlite",
		RootPath:         "/tmp/files",
		InstructionsPath: "/tmp/instructions.md",
		GatewayURL:       "http://127.0.0.1:18789/",
		AgentID:          "default",
	}
}

func TestConfigValidateRequiresInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "database", mutate: func(c *Config) { c.DatabasePath = " " }, want: "database path"},
		{name: "root", mutate: func(c *Config) { c.RootPath = "" }, want: "filesystem root"},
		{name: "instructions", mutate: func(c *Config) { c.InstructionsPath = "" }, want: "instructions path"},
		{name: "Gateway", mutate: func(c *Config) { c.GatewayURL = "" }, want: "Gateway URL"},
		{name: "agent", mutate: func(c *Config) { c.AgentID = "" }, want: "agent ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestConfigValidateGatewayURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "relative", url: "/gateway", want: "http or https"},
		{name: "unsupported scheme", url: "ftp://gateway.example", want: "http or https"},
		{name: "missing host", url: "https:///path", want: "host"},
		{name: "userinfo", url: "https://user:pass@gateway.example", want: "user information"},
		{name: "query", url: "https://gateway.example?x=1", want: "query or fragment"},
		{name: "fragment", url: "https://gateway.example#part", want: "query or fragment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			config.GatewayURL = test.url
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, test.want)
			}
		})
	}

	t.Run("valid HTTP and HTTPS URLs", func(t *testing.T) {
		for _, gatewayURL := range []string{
			"http://127.0.0.1:18789",
			"https://gateway.example/openclaw/",
		} {
			config := validConfig()
			config.GatewayURL = gatewayURL
			if err := config.Validate(); err != nil {
				t.Errorf("Validate(%q) error = %v", gatewayURL, err)
			}
		}
	})
}

func TestNewValidatesAndNormalizesConfig(t *testing.T) {
	config := validConfig()
	config.GatewayURL = " http://127.0.0.1:18789/// "

	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := executor.config.GatewayURL, "http://127.0.0.1:18789"; got != want {
		t.Errorf("GatewayURL = %q, want %q", got, want)
	}
	if executor.config.HTTPClient != http.DefaultClient {
		t.Errorf("HTTPClient = %p, want default client %p", executor.config.HTTPClient, http.DefaultClient)
	}
}

func TestNewKeepsCustomHTTPClient(t *testing.T) {
	config := validConfig()
	customClient := &http.Client{}
	config.HTTPClient = customClient

	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if executor.config.HTTPClient != customClient {
		t.Fatal("New did not preserve the configured HTTP client")
	}
}
