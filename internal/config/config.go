// Package config loads and strictly validates hetdns configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxConfigSize = 1 << 20

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// Duration is a JSON duration represented as a Go duration string.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("must be a duration string")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// Config is the immutable service configuration.
type Config struct {
	Sources map[string]Source `json:"sources"`
	Records map[string]Record `json:"records"`
}

// Source describes an HTTP or external-command address source.
type Source struct {
	Type                 string   `json:"type"`
	Family               string   `json:"family"`
	URL                  string   `json:"url,omitempty"`
	Argv                 []string `json:"argv,omitempty"`
	Timeout              Duration `json:"timeout,omitzero"`
	UserAgent            *string  `json:"user_agent,omitempty"`
	AllowNonPublic       bool     `json:"allow_non_public,omitempty"`
	AllowInsecureHTTP    bool     `json:"allow_insecure_http,omitempty"`
	AllowPrivateNetworks bool     `json:"allow_private_networks,omitempty"`
}

// Record describes one authoritative RRset.
type Record struct {
	Zone       string `json:"zone"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Source     string `json:"source"`
	TTL        uint32 `json:"ttl,omitempty"`
	Create     *bool  `json:"create,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// MayCreate reports whether a missing RRset may be created. The default is true.
func (r Record) MayCreate() bool { return r.Create == nil || *r.Create }

// Load reads and validates a JSON configuration file.
func Load(path string) (*Config, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	if len(data) > maxConfigSize {
		return nil, fmt.Errorf("configuration exceeds %d bytes", maxConfigSize)
	}
	return Parse(data)
}

// Parse strictly decodes and validates configuration data.
func Parse(data []byte) (*Config, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode configuration: multiple JSON values")
		}
		return fmt.Errorf("decode configuration: %w", err)
	}
	return nil
}

// Validate checks all cross-references and safety constraints.
func (c *Config) Validate() error {
	if len(c.Sources) == 0 {
		return errors.New("sources: at least one source is required")
	}
	if len(c.Records) == 0 {
		return errors.New("records: at least one record is required")
	}
	for id, source := range c.Sources {
		if !idPattern.MatchString(id) {
			return fmt.Errorf("sources.%s: invalid source ID", id)
		}
		if err := validateSource(source); err != nil {
			return fmt.Errorf("sources.%s: %w", id, err)
		}
	}
	for id, record := range c.Records {
		if !idPattern.MatchString(id) {
			return fmt.Errorf("records.%s: invalid record ID", id)
		}
		if err := validateRecord(record); err != nil {
			return fmt.Errorf("records.%s: %w", id, err)
		}
		source, ok := c.Sources[record.Source]
		if !ok {
			return fmt.Errorf("records.%s.source: source %q does not exist", id, record.Source)
		}
		wantFamily := "ipv4"
		if record.Type == "AAAA" {
			wantFamily = "ipv6"
		}
		if source.Family != wantFamily {
			return fmt.Errorf(
				"records.%s.source: %s record requires an %s source",
				id,
				record.Type,
				wantFamily,
			)
		}
	}
	return nil
}

func validateSource(source Source) error {
	if source.Family != "ipv4" && source.Family != "ipv6" {
		return errors.New("family: must be ipv4 or ipv6")
	}
	if source.Timeout.Duration == 0 {
		source.Timeout.Duration = 10 * time.Second
	}
	if source.Timeout.Duration < time.Millisecond || source.Timeout.Duration > time.Minute {
		return errors.New("timeout: must be between 1ms and 1m")
	}
	switch source.Type {
	case "http":
		if source.URL == "" {
			return errors.New("url: is required for an http source")
		}
		if len(source.Argv) != 0 {
			return errors.New("argv: is only valid for a command source")
		}
		if source.UserAgent != nil {
			if err := validateUserAgent(*source.UserAgent); err != nil {
				return fmt.Errorf("user_agent: %w", err)
			}
		}
		parsed, err := url.Parse(source.URL)
		if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
			return errors.New("url: must be an absolute URL")
		}
		if parsed.User != nil || parsed.Fragment != "" {
			return errors.New("url: userinfo and fragments are not allowed")
		}
		if parsed.Scheme != "https" && (parsed.Scheme != "http" || !source.AllowInsecureHTTP) {
			return errors.New("url: HTTPS is required unless allow_insecure_http is true")
		}
	case "command":
		if source.URL != "" {
			return errors.New("url: is only valid for an http source")
		}
		if len(source.Argv) == 0 || source.Argv[0] == "" {
			return errors.New("argv: must contain a command")
		}
		if !filepath.IsAbs(source.Argv[0]) {
			return errors.New("argv[0]: executable path must be absolute")
		}
		for i, arg := range source.Argv {
			if strings.IndexByte(arg, 0) >= 0 {
				return fmt.Errorf("argv[%d]: contains a NUL byte", i)
			}
		}
		if source.AllowInsecureHTTP || source.AllowPrivateNetworks {
			return errors.New("HTTP network options are not valid for a command source")
		}
		if source.UserAgent != nil {
			return errors.New("user_agent: is only valid for an http source")
		}
	default:
		return errors.New("type: must be http or command")
	}
	return nil
}

func validateUserAgent(value string) error {
	if len(value) > 256 {
		return errors.New("must not exceed 256 bytes")
	}
	for _, char := range []byte(value) {
		if char < 0x20 || char > 0x7e {
			return errors.New("must contain only visible ASCII characters")
		}
	}
	return nil
}

func validateRecord(record Record) error {
	if !validDomain(record.Zone) {
		return errors.New("zone: must be a valid relative DNS zone name")
	}
	if !validOwner(record.Name) {
		return errors.New("name: must be @ or a valid relative owner name")
	}
	if record.Type != "A" && record.Type != "AAAA" {
		return errors.New("type: must be A or AAAA")
	}
	if record.Source == "" {
		return errors.New("source: is required")
	}
	if record.TTL != 0 && (record.TTL < 60 || record.TTL > 2147483647) {
		return errors.New("ttl: must be zero or between 60 and 2147483647")
	}
	return nil
}

func validDomain(name string) bool {
	return name != "" && len(name) <= 253 && !strings.HasSuffix(name, ".") && validLabels(name, false)
}

func validOwner(name string) bool {
	if name == "@" {
		return true
	}

	return name != "" &&
		len(name) <= 253 &&
		!strings.HasSuffix(name, ".") &&
		validLabels(name, true)
}

func validLabels(name string, wildcard bool) bool {
	if wildcard {
		if name == "*" {
			return true
		}
		name = strings.TrimPrefix(name, "*.")
	}
	for label := range strings.SplitSeq(name, ".") {
		if !validLabel(label) {
			return false
		}
	}
	return true
}

func validLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		if !isLabelChar(label[i]) {
			return false
		}
	}
	return true
}

func isLabelChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z':
	case 'A' <= c && c <= 'Z':
	case '0' <= c && c <= '9':
	case c == '-':
	default:
		return false
	}
	return true
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, "$", make(map[string]struct{})); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	return ensureEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder, path string, _ map[string]struct{}) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return fmt.Errorf("null value at %s is not allowed", path)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate field %q at %s", key, path)
			}
			keys[key] = struct{}{}
			if err := scanJSONValue(decoder, path+"."+key, nil); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index), nil); err != nil {
				return err
			}
			index++
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

// ReadToken reads the preferred token file, falling back to the provided environment value.
func ReadToken(tokenFile, environmentValue string) (string, error) {
	var data []byte
	if tokenFile != "" {
		file, err := os.Open(filepath.Clean(tokenFile))
		if err != nil {
			return "", fmt.Errorf("read Hetzner token file: %w", err)
		}
		data, err = io.ReadAll(io.LimitReader(file, 16*1024+1))
		closeErr := file.Close()
		if err != nil {
			return "", fmt.Errorf("read Hetzner token file: %w", err)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close Hetzner token file: %w", closeErr)
		}
	} else {
		data = []byte(environmentValue)
	}
	if len(data) > 16*1024 {
		return "", errors.New("hetzner token is unreasonably large")
	}

	token := strings.Trim(string(data), " \t\r\n\v\f")
	if token == "" {
		return "", errors.New("hetzner token is required")
	}
	if strings.ContainsAny(token, "\r\n") {
		return "", errors.New("hetzner token contains an invalid line break")
	}
	return token, nil
}
