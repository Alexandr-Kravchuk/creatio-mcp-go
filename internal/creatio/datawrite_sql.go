package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DataWriteSQL executes clio's SQL gateway command and preserves its command log.
func (c *Client) DataWriteSQL(ctx context.Context, script, file, view, destination string, silent bool) CommandResult {
	lines := []LogMessage{}
	if file != "" {
		content, err := os.ReadFile(file)
		if err != nil {
			return CommandFailure(err.Error())
		}
		script = strings.ReplaceAll(string(content), "\n", "|nl|")
		if !silent {
			lines = append(lines, LogMessage{MessageType: "None", Value: string(content)})
		}
	}
	body, _ := json.Marshal(map[string]string{"script": script})
	response, err := c.dataWriteSendOnce(ctx, "POST", "rest/CreatioApiGateway/ExecuteSqlScript", body, 45*time.Second)
	if err != nil {
		return CommandFailure(err.Error())
	}
	// ClioGate may JSON-quote its whole response.
	if strings.HasPrefix(strings.TrimSpace(response), "\"") {
		var unquoted string
		if json.Unmarshal([]byte(response), &unquoted) == nil {
			response = unquoted
		}
	}
	if strings.HasPrefix(response, "ExecuteSQL ERROR: ") {
		return CommandFailure(strings.TrimPrefix(response, "ExecuteSQL ERROR: "))
	}
	result, err := dataWriteSQLRender(response, view, destination)
	if err != nil {
		return CommandFailure(err.Error())
	}
	if destination != "" {
		full, err := filepath.Abs(destination)
		if err != nil {
			return CommandFailure(err.Error())
		}
		lines = append(lines, LogMessage{MessageType: "Info", Value: "Results saved to: " + full})
	}
	if !silent {
		lines = append(lines, LogMessage{MessageType: "None", Value: result})
	}
	lines = append(lines, LogMessage{MessageType: "Info", Value: "Done"})
	return CommandResult{ExitCode: 0, Messages: lines}
}

func dataWriteSQLRender(response, view, destination string) (string, error) {
	if view == "json" {
		var parsed any
		if err := json.Unmarshal([]byte(response), &parsed); err != nil {
			return "", err
		}
		switch parsed.(type) {
		case []any, float64:
		default:
			return "", fmt.Errorf("SQL response must contain a JSON row array or an affected-row count.")
		}
		if destination != "" {
			if err := os.WriteFile(destination, []byte(response), 0644); err != nil {
				return "", err
			}
		}
		return response, nil
	}
	if count, err := strconv.Atoi(response); err == nil {
		if destination != "" {
			return "", fmt.Errorf("Use -v json to export an affected-row count.")
		}
		return fmt.Sprintf("(%d rows affected)", count), nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(response), &rows); err != nil {
		return "", err
	}
	if view == "csv" {
		columns := []string{}
		if len(rows) > 0 {
			for key := range rows[0] {
				columns = append(columns, key)
			}
		}
		// DataTable keeps input column order; this fallback sorts where decoding into a map cannot.
		sort.Strings(columns)
		var b strings.Builder
		b.WriteString(strings.Join(columns, ";") + "\n")
		for _, row := range rows {
			for i, key := range columns {
				if i > 0 {
					b.WriteByte(';')
				}
				b.WriteString(fmt.Sprint(row[key]))
			}
			b.WriteByte('\n')
		}
		if err := os.WriteFile(destination, []byte(b.String()), 0644); err != nil {
			return "", err
		}
	}
	// ConsoleTables output is display-only. Full fidelity is available through view=json.
	result := ""
	if view == "table" && destination != "" {
		if err := os.WriteFile(destination, []byte(result), 0644); err != nil {
			return "", err
		}
	}
	return result, nil
}
