package ossein

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

// staticEnv is a lookup over a literal map.
func staticEnv(values map[string]string) EnvLookup {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadConfigParsesStringLists(t *testing.T) {
	type Config struct {
		Origins []string `env:"ORIGINS"`
		Hosts   []string `env:"HOSTS" default:"localhost,127.0.0.1"`
		Absent  []string `env:"ABSENT"`
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		// Spaced out for readability, with a trailing comma: neither is an error, and
		// neither produces an empty element.
		"ORIGINS": "https://app.test, https://admin.test ,",
	}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}

	want := []string{"https://app.test", "https://admin.test"}
	if len(config.Origins) != len(want) {
		t.Fatalf("Origins = %q, want %q", config.Origins, want)
	}
	for index, origin := range want {
		if config.Origins[index] != origin {
			t.Fatalf("Origins = %q, want %q", config.Origins, want)
		}
	}

	if len(config.Hosts) != 2 || config.Hosts[0] != "localhost" || config.Hosts[1] != "127.0.0.1" {
		t.Fatalf("Hosts = %q, want the default list parsed the same way", config.Hosts)
	}
	if config.Absent != nil {
		t.Fatalf("Absent = %q, want nil for a variable that is not set", config.Absent)
	}
}

func TestLoadConfigParsesTypedLists(t *testing.T) {
	type Config struct {
		Ports    []int           `env:"PORTS"`
		Retries  []uint16        `env:"RETRIES"`
		Weights  []float64       `env:"WEIGHTS"`
		Flags    []bool          `env:"FLAGS"`
		Timeouts []time.Duration `env:"TIMEOUTS"`
		Peers    []netip.Addr    `env:"PEERS"`
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"PORTS":    "8080,8443",
		"RETRIES":  "1,2,3",
		"WEIGHTS":  "0.5,1.5",
		"FLAGS":    "true,false",
		"TIMEOUTS": "5s,1m",
		"PEERS":    "10.0.0.1,2001:db8::1",
	}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}

	if len(config.Ports) != 2 || config.Ports[0] != 8080 || config.Ports[1] != 8443 {
		t.Fatalf("Ports = %v", config.Ports)
	}
	if len(config.Retries) != 3 || config.Retries[2] != 3 {
		t.Fatalf("Retries = %v", config.Retries)
	}
	if len(config.Weights) != 2 || config.Weights[1] != 1.5 {
		t.Fatalf("Weights = %v", config.Weights)
	}
	if len(config.Flags) != 2 || !config.Flags[0] || config.Flags[1] {
		t.Fatalf("Flags = %v", config.Flags)
	}
	// The element parser is the same one fields use, so a list of durations works
	// without the loader knowing about lists of durations.
	if len(config.Timeouts) != 2 || config.Timeouts[0] != 5*time.Second || config.Timeouts[1] != time.Minute {
		t.Fatalf("Timeouts = %v", config.Timeouts)
	}
	// And so does a list of anything that can parse itself.
	if len(config.Peers) != 2 || config.Peers[0].String() != "10.0.0.1" || config.Peers[1].String() != "2001:db8::1" {
		t.Fatalf("Peers = %v", config.Peers)
	}
}

// TestLoadConfigTreatsByteSlicesAsRawValues is the one slice that must not be split. A
// signing key or a shared secret is bytes, and a comma in it is data.
func TestLoadConfigTreatsByteSlicesAsRawValues(t *testing.T) {
	type Config struct {
		Secret []byte `env:"SECRET"`
	}

	const raw = "s3cr3t,with,commas"
	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{"SECRET": raw}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if string(config.Secret) != raw {
		t.Fatalf("Secret = %q, want the value untouched", config.Secret)
	}
}

// TestLoadConfigTreatsUint8SlicesAsBytesToo records a trap rather than a decision.
// byte is an alias for uint8, so []uint8 and []byte are the same type and no loader can
// tell them apart: a field declared []uint8 in the hope of a numeric list gets the raw
// bytes of the value. Use a wider integer type for a list of small numbers.
func TestLoadConfigTreatsUint8SlicesAsBytesToo(t *testing.T) {
	type Config struct {
		Numbers []uint8 `env:"NUMBERS"`
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{"NUMBERS": "1,2,3"}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if string(config.Numbers) != "1,2,3" {
		t.Fatalf("Numbers = %v, want the raw bytes of the value", config.Numbers)
	}
}

// TestLoadConfigHonoursTextUnmarshaler covers the escape hatch that keeps the loader
// from needing a case per type. slog.Level and net.IP are standard-library proof; the
// application type is the point.
func TestLoadConfigHonoursTextUnmarshaler(t *testing.T) {
	type Config struct {
		Level  slog.Level  `env:"LOG_LEVEL"`
		Listen netip.Addr  `env:"LISTEN"`
		Bind   net.IP      `env:"BIND"`
		Region regionCode  `env:"REGION"`
		Moment time.Time   `env:"MOMENT"`
		Custom *regionCode `env:"CUSTOM"`
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"LOG_LEVEL": "warn",
		"LISTEN":    "192.168.0.10",
		"BIND":      "::1",
		"REGION":    "eu-west",
		"MOMENT":    "2026-08-01T10:00:00Z",
		"CUSTOM":    "us-east",
	}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}

	if config.Level != slog.LevelWarn {
		t.Fatalf("Level = %v, want warn", config.Level)
	}
	if config.Listen.String() != "192.168.0.10" {
		t.Fatalf("Listen = %v", config.Listen)
	}
	if config.Bind.String() != "::1" {
		t.Fatalf("Bind = %v", config.Bind)
	}
	// A named string type parses itself rather than being assigned as its underlying
	// string, which is what makes validation in UnmarshalText worth writing.
	if config.Region != "EU-WEST" {
		t.Fatalf("Region = %q, want the type's own parsing to have run", config.Region)
	}
	if !config.Moment.Equal(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("Moment = %v", config.Moment)
	}
	if config.Custom == nil || *config.Custom != "US-EAST" {
		t.Fatalf("Custom = %v, want an allocated pointer", config.Custom)
	}
}

// TestLoadConfigReportsATextUnmarshalerError keeps a type's own validation visible
// instead of silently accepting the value.
func TestLoadConfigReportsATextUnmarshalerError(t *testing.T) {
	type Config struct {
		Region regionCode `env:"REGION"`
		Level  slog.Level `env:"LOG_LEVEL"`
	}

	_, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"REGION":    "mars",
		"LOG_LEVEL": "chatty",
	}))
	if err == nil {
		t.Fatal("expected the types' own parsing to reject both values")
	}
	if !errors.Is(err, errUnknownRegion) {
		t.Fatalf("error = %v, want the type's error preserved", err)
	}
	for _, expected := range []string{`Region (REGION): invalid`, `Level (LOG_LEVEL): invalid`} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error does not contain %q: %v", expected, err)
		}
	}
}

func TestLoadConfigParsesURLs(t *testing.T) {
	type Config struct {
		Base     url.URL  `env:"BASE_URL"`
		Upstream *url.URL `env:"UPSTREAM_URL"`
		Absent   *url.URL `env:"ABSENT_URL"`
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"BASE_URL":     "https://api.test/v1",
		"UPSTREAM_URL": "http://127.0.0.1:9000",
	}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}

	if config.Base.Scheme != "https" || config.Base.Host != "api.test" || config.Base.Path != "/v1" {
		t.Fatalf("Base = %+v", config.Base)
	}
	if config.Upstream == nil || config.Upstream.Host != "127.0.0.1:9000" {
		t.Fatalf("Upstream = %v", config.Upstream)
	}
	if config.Absent != nil {
		t.Fatalf("Absent = %v, want nil for a variable that is not set", config.Absent)
	}
}

func TestLoadConfigReportsAnInvalidURL(t *testing.T) {
	type Config struct {
		Base url.URL `env:"BASE_URL"`
	}

	_, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"BASE_URL": "http://[::1",
	}))
	if err == nil {
		t.Fatal("expected an error for a malformed URL")
	}
	if !strings.Contains(err.Error(), "Base (BASE_URL): invalid URL") {
		t.Fatalf("error = %v", err)
	}
}

// TestLoadConfigRejectsNestedLists keeps an unsupported shape an error rather than
// something that appears to work: there is no second separator to split on, so every
// element would receive the whole value.
func TestLoadConfigRejectsNestedLists(t *testing.T) {
	type Config struct {
		Matrix [][]string `env:"MATRIX"`
	}

	_, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{"MATRIX": "a,b"}))
	if err == nil {
		t.Fatal("expected a nested list to be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported field type [][]string") {
		t.Fatalf("error = %v", err)
	}
}

// TestLoadConfigSkipsUnexportedFieldsAndReportsNestedErrors covers two paths the suite
// never reached. An unexported field cannot be set through reflection, so a tag on one
// has to be ignored rather than panic; and an error inside an embedded configuration
// struct has to reach the caller with the path that locates it.
func TestLoadConfigSkipsUnexportedFieldsAndReportsNestedErrors(t *testing.T) {
	type Database struct {
		Port int `env:"DB_PORT"`
	}
	type Config struct {
		Database Database
		Name     string `env:"NAME"`
		secret   string `env:"SECRET"` //nolint:unused // the point of the test
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"NAME":    "svc",
		"SECRET":  "ignored",
		"DB_PORT": "not-a-port",
	}))
	if err == nil {
		t.Fatal("expected the nested field's parse error")
	}
	if !strings.Contains(err.Error(), "config Database.Port (DB_PORT): invalid integer") {
		t.Fatalf("error = %v, want the nested field path", err)
	}
	if config.Name != "svc" {
		t.Fatalf("Name = %q; an unexported field must not stop the rest loading", config.Name)
	}
	if config.secret != "" {
		t.Fatalf("secret = %q, want an unexported field left alone", config.secret)
	}
}

// TestLoadConfigRejectsAPointerWithoutTextUnmarshaler keeps the pointer support narrow:
// it exists so a self-parsing type can be held by pointer, not to make every pointer
// field loadable. A *int would otherwise be assigned an address and left at zero.
func TestLoadConfigRejectsAPointerWithoutTextUnmarshaler(t *testing.T) {
	type Config struct {
		Retries *int `env:"RETRIES"`
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{"RETRIES": "3"}))
	if err == nil {
		t.Fatalf("expected *int to be rejected, got %v", config.Retries)
	}
	if !strings.Contains(err.Error(), "unsupported field type *int") {
		t.Fatalf("error = %v", err)
	}
	if config.Retries != nil {
		t.Fatalf("Retries = %v, want the field left alone", config.Retries)
	}
}

// regionCode is an application type that parses and validates itself, which is the
// case TextUnmarshaler support exists for.
type regionCode string

var errUnknownRegion = errors.New("unknown region")

func (r *regionCode) UnmarshalText(text []byte) error {
	switch code := strings.ToUpper(string(text)); code {
	case "EU-WEST", "US-EAST":
		*r = regionCode(code)
		return nil
	default:
		return errUnknownRegion
	}
}

// TestLoadConfigParsesListsOfSelfParsingSliceTypes covers the element type that broke
// the first version of this: net.IP is a []byte that parses itself, so deciding what a
// list means by kind alone rejected a list of trusted proxies — while a single one
// loaded fine, which is the kind of inconsistency nobody discovers until deployment.
func TestLoadConfigParsesListsOfSelfParsingSliceTypes(t *testing.T) {
	type Config struct {
		Proxy   net.IP   `env:"PROXY"`
		Proxies []net.IP `env:"PROXIES"`
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"PROXY":   "10.0.0.1",
		"PROXIES": "10.0.0.1, 10.0.0.2",
	}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}

	if config.Proxy.String() != "10.0.0.1" {
		t.Fatalf("Proxy = %v", config.Proxy)
	}
	if len(config.Proxies) != 2 {
		t.Fatalf("Proxies = %v, want two addresses", config.Proxies)
	}
	if config.Proxies[0].String() != "10.0.0.1" || config.Proxies[1].String() != "10.0.0.2" {
		t.Fatalf("Proxies = %v", config.Proxies)
	}
}

// TestLoadConfigRejectsAnEmptyOrSchemeShapedURL covers the two values url.Parse accepts
// that produce a URL which is silently useless. Both are realistic operator edits of a
// default like "http://localhost:8080", and both survive to the point where a link is
// built from them and comes out wrong rather than failing.
func TestLoadConfigRejectsAnEmptyOrSchemeShapedURL(t *testing.T) {
	type Config struct {
		Base *url.URL `env:"BASE_URL" default:"http://localhost:8080"`
	}

	// The shape a dropped scheme produces: "localhost" is read as the scheme and
	// "8080" as an opaque body, so the URL has no host and JoinPath discards whatever
	// is appended to it.
	_, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"BASE_URL": "localhost:8080",
	}))
	if err == nil {
		t.Fatal("a scheme-shaped value was accepted as a URL")
	}
	if !strings.Contains(err.Error(), `"localhost" is read as a scheme`) {
		t.Fatalf("error = %v, want it to explain the shape", err)
	}
	if !strings.Contains(err.Error(), "http://localhost:8080") {
		t.Fatalf("error = %v, want it to suggest the corrected value", err)
	}

	// Set-but-empty bypasses the default tag, which only applies when the variable is
	// absent, so without this check the field would be a non-nil empty URL.
	_, err = LoadConfigFromEnv[Config](staticEnv(map[string]string{"BASE_URL": ""}))
	if err == nil {
		t.Fatal("an empty value was accepted as a URL")
	}
	if !strings.Contains(err.Error(), "Base (BASE_URL): a URL is required") {
		t.Fatalf("error = %v", err)
	}

	// A URL with a path but no scheme stays legal: a base path is a real thing to
	// configure, and unlike the opaque form it extends correctly.
	relative, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"BASE_URL": "/api/v1",
	}))
	if err != nil {
		t.Fatalf("a path-only URL should load: %v", err)
	}
	if got := relative.Base.JoinPath("links").String(); got != "/api/v1/links" {
		t.Fatalf("JoinPath = %q", got)
	}
}

// TestLoadConfigRequiredListRejectsAValueWithNoEntries closes the hole between the
// required check and the parse: ",," is not an empty value, so a required allowlist
// would load with nothing in it and every origin would be denied at runtime instead of
// the mistake being caught at startup.
func TestLoadConfigRequiredListRejectsAValueWithNoEntries(t *testing.T) {
	type Config struct {
		Origins []string `env:"ORIGINS" required:"true"`
	}

	_, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{"ORIGINS": " , , "}))
	if err == nil {
		t.Fatal("a required list parsed to no entries and was accepted")
	}
	if !strings.Contains(err.Error(), "Origins (ORIGINS) is required, but") {
		t.Fatalf("error = %v", err)
	}

	// A value with one real entry satisfies it.
	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"ORIGINS": "https://app.test,",
	}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if len(config.Origins) != 1 {
		t.Fatalf("Origins = %q", config.Origins)
	}
}

// TestLoadConfigDistinguishesAnUnsetListFromAnEmptyOne pins semantics a caller can
// legitimately read: nil means the variable was never set, an empty non-nil slice means
// it was set to nothing. Without a test either could silently become the other.
func TestLoadConfigDistinguishesAnUnsetListFromAnEmptyOne(t *testing.T) {
	type Config struct {
		Unset   []string `env:"UNSET"`
		Emptied []string `env:"EMPTIED"`
	}

	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{"EMPTIED": ","}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}

	if config.Unset != nil {
		t.Fatalf("Unset = %#v, want nil for a variable that was never set", config.Unset)
	}
	if config.Emptied == nil {
		t.Fatalf("Emptied = nil, want an empty slice for a variable set to no entries")
	}
	if len(config.Emptied) != 0 {
		t.Fatalf("Emptied = %q, want no entries", config.Emptied)
	}
}

// TestLoadConfigNumbersListElementsAsWritten pins which convention the error uses. The
// alternative — counting surviving entries — would name element 2 for a value whose
// second field is empty, sending an operator to the wrong place.
func TestLoadConfigNumbersListElementsAsWritten(t *testing.T) {
	type Config struct {
		Ports []int `env:"PORTS"`
	}

	_, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{"PORTS": ",8080,nope"}))
	if err == nil {
		t.Fatal("expected the bad element to be reported")
	}
	if !strings.Contains(err.Error(), `element 3: invalid integer "nope"`) {
		t.Fatalf("error = %v, want the position as written in the value", err)
	}
}

// TestLoadConfigKeepsByteSlicesExactly guards the claim that []byte is the raw value:
// not trimmed, not split, not reordered.
func TestLoadConfigKeepsByteSlicesExactly(t *testing.T) {
	type Config struct {
		Key     []byte   `env:"KEY"`
		Nibbles []nibble `env:"NIBBLES"`
	}

	const raw = "  padded , key\t"
	config, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{
		"KEY":     raw,
		"NIBBLES": "1,2,3",
	}))
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if string(config.Key) != raw {
		t.Fatalf("Key = %q, want the value byte for byte", config.Key)
	}
	// A named byte type is not []byte, so it is a list of numbers. The exact type
	// comparison is what makes that work.
	if len(config.Nibbles) != 3 || config.Nibbles[2] != 3 {
		t.Fatalf("Nibbles = %v, want a list of numbers", config.Nibbles)
	}
}

// TestLoadConfigRejectsSlicesOfMaps keeps the message pointed at the element rather
// than silently accepting a shape with no encoding.
func TestLoadConfigRejectsSlicesOfMaps(t *testing.T) {
	type Config struct {
		Rules []map[string]string `env:"RULES"`
	}

	_, err := LoadConfigFromEnv[Config](staticEnv(map[string]string{"RULES": "a=b"}))
	if err == nil {
		t.Fatal("expected a slice of maps to be rejected")
	}
	if !strings.Contains(err.Error(), "element 1: unsupported field type map[string]string") {
		t.Fatalf("error = %v", err)
	}
}

// nibble is a named byte type: distinct from byte, so a list of them is a list of
// numbers rather than a raw value.
type nibble byte
