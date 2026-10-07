package fitting

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/ollama"
)

// EndpointInput carries the two Ollama settings that people confuse.
// FitEnv is OLLAMA_BASE_URL, the URL fitr uses. CLIEnv is OLLAMA_HOST,
// the listen address the Ollama CLI uses. They are not interchangeable.
type EndpointInput struct {
	Explicit string
	FitEnv   string
	CLIEnv   string
}

// Endpoint is the one runtime a fitting will keep. Loopback is not evidence
// that inference ran locally.
type Endpoint struct {
	URL      string
	Source   string
	Locality string
	Note     string
}

// ResolveEndpoint chooses one client URL without probing other providers.
// An explicit URL wins. A conflict between the two environment variables is
// reported rather than resolved by trying whichever answers first.
func ResolveEndpoint(in EndpointInput) (Endpoint, error) {
	explicit, err := canonicalClientURL(in.Explicit)
	if err != nil {
		return Endpoint{}, err
	}
	if explicit != "" {
		// Unused environment settings may explain a valid conflict, but cannot
		// invalidate the operator's explicit client URL.
		fitURL, _ := canonicalClientURL(in.FitEnv)
		cli, bindAll, _ := canonicalHost(in.CLIEnv)
		return explicitEndpoint(explicit, fitURL, cli, bindAll), nil
	}
	fitURL, err := canonicalClientURL(in.FitEnv)
	if err != nil {
		return Endpoint{}, fmt.Errorf("OLLAMA_BASE_URL: %w", err)
	}
	cli, cliBindAll, err := canonicalHost(in.CLIEnv)
	if err != nil {
		return Endpoint{}, fmt.Errorf("OLLAMA_HOST: %w", err)
	}
	if fitURL != "" && cli != "" && !cliBindAll && !sameClient(fitURL, cli) {
		return Endpoint{}, errors.New("OLLAMA_BASE_URL and OLLAMA_HOST point at different addresses. fitr uses OLLAMA_BASE_URL; the Ollama CLI uses OLLAMA_HOST. Pass --endpoint or set one client URL")
	}
	if fitURL != "" {
		return Endpoint{URL: fitURL, Source: "OLLAMA_BASE_URL", Locality: locality(fitURL),
			Note: "fitr uses OLLAMA_BASE_URL. OLLAMA_HOST does not replace it. Loopback is not proof of local inference."}, nil
	}
	if cli != "" && !cliBindAll && locality(cli) != "loopback-unproven" {
		return Endpoint{}, errors.New("OLLAMA_HOST is set but fitr does not use it. Pass --endpoint or set OLLAMA_BASE_URL to the client URL")
	}
	note := "No endpoint was set. fitr is using the default Ollama client URL. Loopback is not proof of local inference."
	if cli != "" || cliBindAll {
		note = "OLLAMA_HOST is the Ollama listen address. fitr is using its default client URL and did not adopt OLLAMA_HOST. Loopback is not proof of local inference."
	}
	return Endpoint{URL: ollama.DefaultURL, Source: "default", Locality: "loopback-unproven", Note: note}, nil
}

func explicitEndpoint(explicit, fitURL, cli string, cliBindAll bool) Endpoint {
	note := "Loopback is not proof of local inference. Remote-marker checks still apply before a local claim."
	if locality(explicit) != "loopback-unproven" {
		note = "This is not a loopback endpoint. Remote-marker checks still apply, and fitr will not switch providers."
	}
	switch {
	case fitURL != "" && !sameClient(explicit, fitURL):
		note = "The explicit endpoint is kept. OLLAMA_BASE_URL is different and is not used. " + note
	case cli != "" && !cliBindAll && !sameClient(explicit, cli):
		note = "The explicit endpoint is kept. OLLAMA_HOST is different and does not select fitr. " + note
	}
	return Endpoint{URL: explicit, Source: "explicit", Locality: locality(explicit), Note: note}
}

func sameClient(left, right string) bool {
	if left == right {
		return true
	}
	a, aErr := url.Parse(left)
	b, bErr := url.Parse(right)
	if aErr != nil || bErr != nil {
		return false
	}
	if a.Scheme != b.Scheme || a.EscapedPath() != b.EscapedPath() {
		return false
	}
	if endpointPort(a) != endpointPort(b) {
		return false
	}
	return loopbackHost(a.Hostname()) && loopbackHost(b.Hostname())
}

func endpointPort(parsed *url.URL) string {
	if port := parsed.Port(); port != "" {
		return port
	}
	if parsed.Scheme == "https" {
		return "443"
	}
	return "80"
}

func loopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func canonicalClientURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("endpoint must be an http or https URL")
	}
	if parsed.User != nil {
		return "", errors.New("endpoint must not carry userinfo; credentials stay out of the plan")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("endpoint must not carry a query or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func canonicalHost(value string) (string, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, nil
	}
	if strings.Contains(value, "://") {
		parsed, err := canonicalClientURL(value)
		return parsed, false, err
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		host = value
		port = "11434"
	}
	bindAll := host == "0.0.0.0" || host == "::" || host == "[::]"
	if bindAll {
		return "", true, nil
	}
	parsed, err := canonicalClientURL("http://" + net.JoinHostPort(host, port))
	return parsed, false, err
}

func locality(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "remote-endpoint"
	}
	if loopbackHost(parsed.Hostname()) {
		return "loopback-unproven"
	}
	return "remote-endpoint"
}

// ApplyToRuntime copies identity from an inspected installation and settings
// from the fitting plan. A ceiling cannot become ReserveBytes.
func ApplyToRuntime(plan Plan, inspected autoruntime.Spec) (autoruntime.Spec, error) {
	if err := plan.Validate(); err != nil {
		return autoruntime.Spec{}, err
	}
	if !plan.OwnedRuntime || plan.CapacityKind != CapacityReserve {
		return autoruntime.Spec{}, errors.New("owned runtime settings are available only for an approved reserve policy")
	}
	if plan.Blocked {
		return autoruntime.Spec{}, errors.New("a blocked fitting does not launch a runtime")
	}
	out := inspected
	out.NumCtx = plan.ContextTokens
	out.ReserveBytes = plan.CapacityBytes
	out.KVCacheType = plan.KVCacheType
	out.FlashAttention = plan.FlashAttention
	if err := out.Validate(); err != nil {
		return autoruntime.Spec{}, err
	}
	if out.ReserveBytes != plan.CapacityBytes || out.NumCtx != plan.ContextTokens {
		return autoruntime.Spec{}, errors.New("owned runtime did not keep the fitting context and reserve")
	}
	return out, nil
}
