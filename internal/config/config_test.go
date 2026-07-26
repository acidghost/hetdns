package config

import (
	"strings"
	"testing"
)

const validConfiguration = `{
  "sources": {
    "wan": {
      "type": "http",
      "family": "ipv4",
      "url": "https://example.com/ip",
      "timeout": "2s"
    }
  },
  "records": {
    "home": {
      "zone": "example.com",
      "name": "home",
      "type": "A",
      "source": "wan",
      "ttl": 300
    }
  }
}`

const relativeExecutableConfiguration = `{
  "sources": {
    "x": {
      "type": "command",
      "family": "ipv4",
      "argv": ["echo"]
    }
  },
  "records": {
    "x": {
      "zone": "example.com",
      "name": "x",
      "type": "A",
      "source": "x"
    }
  }
}`

func TestParseValidAndDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]byte(validConfiguration))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Records["home"].MayCreate() {
		t.Fatal("create should default to true")
	}
}

func TestParseStrictFailures(t *testing.T) {
	t.Parallel()

	unknownField := strings.Replace(
		validConfiguration,
		`"timeout": "2s"`,
		`"timeout": "2s", "secret": "x"`,
		1,
	)
	familyMismatch := strings.Replace(
		validConfiguration,
		`"type": "A"`,
		`"type": "AAAA"`,
		1,
	)
	tests := map[string]string{
		"duplicate":           `{"sources":{},"sources":{},"records":{}}`,
		"unknown":             unknownField,
		"family mismatch":     familyMismatch,
		"relative executable": relativeExecutableConfiguration,
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := Parse([]byte(input)); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestHTTPUserAgentValidation(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"disabled": "",
		"custom":   "custom-client/1.0",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := strings.Replace(
				validConfiguration,
				`"timeout": "2s"`,
				`"timeout": "2s", "user_agent": "`+value+`"`,
				1,
			)
			cfg, err := Parse([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Sources["wan"].UserAgent == nil || *cfg.Sources["wan"].UserAgent != value {
				t.Fatalf("user_agent was not preserved: %#v", cfg.Sources["wan"].UserAgent)
			}
		})
	}

	invalid := strings.Replace(
		validConfiguration,
		`"timeout": "2s"`,
		`"timeout": "2s", "user_agent": "bad\nvalue"`,
		1,
	)
	if _, err := Parse([]byte(invalid)); err == nil {
		t.Fatal("accepted a User-Agent containing a control character")
	}
}

func TestCreateCanBeDisabled(t *testing.T) {
	t.Parallel()

	input := strings.Replace(
		validConfiguration,
		`"ttl": 300`,
		`"ttl": 300, "create": false`,
		1,
	)
	cfg, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Records["home"].MayCreate() {
		t.Fatal("create=false was not preserved")
	}
}
