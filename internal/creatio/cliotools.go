package creatio

import (
	"os"
	"path/filepath"
	"strings"
)

// ClioToolsDirectoryVariable names a directory that replaces ~/.dotnet/tools as the place of the clio .NET
// tool whose bundled archives (CrtProcessBuilder, cliogate, dashboards migrator) and help files this server
// reads. It holds .store/clio/<version>/... as `dotnet tool install --tool-path <dir> clio` lays it out. The
// parity harness sets it to the tool path of the clio it compares against, so both servers use one bundle.
const ClioToolsDirectoryVariable = "MCPGO_CLIO_TOOLS_DIR"

// ClioToolsDirectory is the directory that holds clio's .store: the override, else ~/.dotnet/tools; "" when
// neither can be determined.
func ClioToolsDirectory() string {
	if override := strings.TrimSpace(os.Getenv(ClioToolsDirectoryVariable)); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".dotnet", "tools")
}
