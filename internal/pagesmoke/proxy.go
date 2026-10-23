package pagesmoke

import (
	"net/http"
	"net/url"
	"strings"
)

// downloadTransport is the client of the headless-shell download (AmbientProxy): the proxy HTTPS_PROXY /
// HTTP_PROXY name (either spelling), with NO_PROXY honoured. clio falls back to the operating system's proxy
// settings (WinHTTP or PAC on Windows, System Configuration on macOS) when neither variable is set; Go has no
// reader for those, so here the download then goes direct. A user:password in the variable is sent to that proxy
// only, as clio does.
func downloadTransport(env environmentReader) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(request *http.Request) (*url.URL, error) {
		return ambientProxyFor(env, request.URL), nil
	}
	return transport
}

// ambientProxyFor is AmbientProxy.GetProxy plus IsBypassed: nil means direct.
func ambientProxyFor(env environmentReader, destination *url.URL) *url.URL {
	if noProxy := readProxyVariable(env, "NO_PROXY"); noProxy != "" {
		for _, entry := range strings.Split(noProxy, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if entry == "*" || noProxyMatches(destination.Hostname(), entry) {
				return nil
			}
		}
	}
	configured := readProxyVariable(env, "HTTP_PROXY")
	if destination.Scheme == "https" {
		if secure := readProxyVariable(env, "HTTPS_PROXY"); secure != "" {
			configured = secure
		}
	}
	if configured == "" {
		return nil
	}
	if !strings.Contains(configured, "://") {
		configured = "http://" + configured
	}
	proxy, err := url.Parse(configured)
	if err != nil || proxy.Host == "" {
		return nil
	}
	return proxy
}

// noProxyMatches: "corp.example" and ".corp.example" both cover the host itself and any subdomain.
func noProxyMatches(host, entry string) bool {
	domain := strings.TrimLeft(entry, ".")
	return strings.EqualFold(host, domain) || strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(domain))
}

// readProxyVariable reads NAME, then name, ignoring a blank value.
func readProxyVariable(env environmentReader, name string) string {
	value := env(name)
	if blank(value) {
		value = env(strings.ToLower(name))
	}
	if blank(value) {
		return ""
	}
	return strings.TrimSpace(value)
}
