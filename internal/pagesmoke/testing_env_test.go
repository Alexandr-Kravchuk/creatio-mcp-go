package pagesmoke

import "github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"

func envFor(uri, login, secret string) creatio.PageSmokeEnvironment {
	env := creatio.PageSmokeEnvironment{URI: uri, Login: login}
	env.Password = secret
	return env
}
