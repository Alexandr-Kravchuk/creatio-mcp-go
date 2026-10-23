package creatio

// The logins reg-web-app performs on its own: check-login after a registration, and the authenticated probe
// of the runtime detection. Both run on a connection built from the registration alone, as clio builds an
// owned Creatio.Client for them, and each failure carries the two texts clio shows: the exception's message
// (check-login) and its innermost exception's message (the detection's probe summary).

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// RegistrationTarget is the connection a registration describes.
type RegistrationTarget struct {
	URI, Login, Password, ClientID, ClientSecret, AuthAppURI string
	IsNetCore                                                bool
}

// loginFailure is a failed login or request: outer is the exception clio's command logs, base the innermost
// exception its probe summary names.
type loginFailure struct{ outer, base string }

func (f *loginFailure) Error() string { return f.outer }

// BaseMessage is the innermost exception's message for a failure from this file, the text otherwise.
func BaseMessage(err error) string {
	var failure *loginFailure
	if errors.As(err, &failure) {
		return failure.base
	}
	return err.Error()
}

// registrationTransport trusts any certificate, as Creatio.Client and clio's probe clients do.
func registrationTransport() http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- clio's clients accept any certificate
	return transport
}

// RegistrationTransport is the transport reg-web-app's logins and probes use; tests replace it.
var RegistrationTransport = registrationTransport()

type registrationSession struct {
	client *http.Client
	csrf   string
	bearer string
}

// CheckRegistrationLogin is reg-web-app's check-login: one login with the registration's credentials.
func CheckRegistrationLogin(ctx context.Context, target RegistrationTarget) error {
	_, err := registrationLogin(ctx, target, 100*time.Second)
	return err
}

func registrationLogin(ctx context.Context, target RegistrationTarget, timeout time.Duration) (*registrationSession, error) {
	base := strings.TrimRight(strings.TrimSpace(target.URI), "/")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: RegistrationTransport, Jar: jar, Timeout: timeout}
	if target.ClientID != "" {
		return registrationToken(ctx, client, target)
	}
	_, offset := time.Now().Zone()
	body, _ := json.Marshal(map[string]any{"UserName": target.Login, "UserPassword": target.Password, "TimeZoneOffset": -offset / 60})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/ServiceModel/AuthService.svc/Login", bytes.NewReader(body))
	if err != nil {
		return nil, &loginFailure{err.Error(), err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, transportFailure(err, false)
	}
	payload, _ := readResponseLimit(response, maxResponseBytes)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, &loginFailure{
			outer: fmt.Sprintf("The remote server returned an error: (%d) %s.", response.StatusCode, reasonPhrase(response)),
			base:  fmt.Sprintf("The authentication endpoint returned HTTP %d %s.", response.StatusCode, reasonPhrase(response)),
		}
	}
	var answer struct {
		Code *int `json:"Code"`
	}
	if json.Unmarshal(payload, &answer) != nil || answer.Code == nil || *answer.Code != 0 {
		// clio's text also carries its login diagnostics in brackets (client and timing counters).
		message := "Unauthorized " + target.Login + " for " + target.URI
		return nil, &loginFailure{message, message}
	}
	session := &registrationSession{client: client}
	if parsed, err := url.Parse(base + "/"); err == nil {
		for _, cookie := range jar.Cookies(parsed) {
			if strings.EqualFold(cookie.Name, "BPMCSRF") {
				session.csrf = cookie.Value
			}
		}
	}
	return session, nil
}

// registrationToken is the client-credentials login: the form body clio's OAuth client posts to AuthAppUri.
func registrationToken(ctx context.Context, client *http.Client, target RegistrationTarget) (*registrationSession, error) {
	form := url.Values{"client_id": {target.ClientID}, "client_secret": {target.ClientSecret}, "grant_type": {"client_credentials"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.AuthAppURI, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, &loginFailure{"One or more errors occurred. (" + err.Error() + ")", err.Error()}
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return nil, transportFailure(err, true)
	}
	payload, _ := readResponseLimit(response, maxResponseBytes)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		inner := fmt.Sprintf("Response status code does not indicate success: %d (%s).", response.StatusCode, reasonPhrase(response))
		return nil, &loginFailure{"One or more errors occurred. (" + inner + ")", inner}
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(payload, &token) != nil || token.AccessToken == "" {
		message := "OAuth token endpoint returned no access_token"
		return nil, &loginFailure{"One or more errors occurred. (" + message + ")", message}
	}
	return &registrationSession{client: client, bearer: token.AccessToken}, nil
}

// transportFailure words a request that got no HTTP answer as .NET does: the socket sentence with the
// endpoint (inside an AggregateException for the OAuth client), and the bare sentence as the innermost one.
func transportFailure(err error, aggregate bool) error {
	sentence, outer := "", ""
	if fault := newConnectFault(err); fault != nil {
		sentence, outer = fault.sentence, fault.Error()
	} else if isTimeout(err) {
		sentence, outer = "A task was canceled.", "The request was canceled due to the configured HttpClient.Timeout elapsing."
	} else {
		sentence, outer = err.Error(), err.Error()
	}
	if aggregate {
		outer = "One or more errors occurred. (" + outer + ")"
	}
	return &loginFailure{outer, sentence}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
}

// reasonPhrase is HttpResponseMessage.ReasonPhrase: the server's status text.
func reasonPhrase(response *http.Response) string {
	return strings.TrimSpace(strings.TrimPrefix(response.Status, fmt.Sprint(response.StatusCode)))
}

// socketSentence is the innermost exception's message for a request that got no answer.
func socketSentence(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if sentence := connectSentence(urlErr.Err, runtime.GOOS); sentence != "" {
			return sentence
		}
	}
	if isTimeout(err) {
		return "A task was canceled."
	}
	return err.Error()
}
