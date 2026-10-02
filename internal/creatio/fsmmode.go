package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// FsmStaticFileContent is the staticFileContent object GetApplicationInfo reports when FSM mode is off.
type FsmStaticFileContent struct {
	SchemasRuntimePath   *string `json:"schemasRuntimePath,omitempty"`
	ResourcesRuntimePath *string `json:"resourcesRuntimePath,omitempty"`
}

// FsmModeResult is the get-fsm-mode answer. clio also echoes the registered environment name; this server
// has none, its target is CREATIO_URL.
type FsmModeResult struct {
	Mode                 string                `json:"mode"`
	UseStaticFileContent bool                  `json:"useStaticFileContent"`
	StaticFileContent    *FsmStaticFileContent `json:"staticFileContent"`
}

// GetFsmMode derives file system mode from ApplicationInfoService.svc/GetApplicationInfo the way clio's
// FsmModeStatusService does: useStaticFileContent=false with staticFileContent=null is "on",
// useStaticFileContent=true with a populated staticFileContent is "off", anything else is an error.
func (c *Client) GetFsmMode(ctx context.Context) (FsmModeResult, error) {
	// clio posts an empty body to this route.
	response, err := c.callEnvironmentRoute(ctx, http.MethodPost, applicationInfoRoute, []byte{}, 0)
	if err != nil {
		return FsmModeResult{}, fmt.Errorf("GetApplicationInfo request failed: %w", err)
	}
	if response.needsReLogin || response.status == http.StatusUnauthorized || response.status == http.StatusForbidden {
		return FsmModeResult{}, errors.New("GetApplicationInfo rejected the configured credentials.")
	}
	if response.status < http.StatusOK || response.status >= http.StatusMultipleChoices {
		return FsmModeResult{}, fmt.Errorf("GetApplicationInfo returned HTTP %d.", response.status)
	}
	return runtimeFsmParse(response.payload)
}

func runtimeFsmParse(payload []byte) (FsmModeResult, error) {
	if strings.TrimSpace(string(payload)) == "" {
		return FsmModeResult{}, errors.New("GetApplicationInfo returned an empty response.")
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return FsmModeResult{}, fmt.Errorf("GetApplicationInfo returned invalid JSON: %v", err)
	}
	var candidates []map[string]any
	runtimeFsmCollectCandidates(root, &candidates)
	switch len(candidates) {
	case 0:
		return FsmModeResult{}, errors.New("GetApplicationInfo response does not contain a canonical payload with both 'useStaticFileContent' and 'staticFileContent'.")
	case 1:
	default:
		return FsmModeResult{}, errors.New("GetApplicationInfo response contains multiple payload candidates with 'useStaticFileContent' and 'staticFileContent'.")
	}
	info := candidates[0]
	useStatic, _ := runtimeFsmProperty(info, "useStaticFileContent")
	staticContent, _ := runtimeFsmProperty(info, "staticFileContent")
	useStaticFileContent, ok := useStatic.(bool)
	if !ok {
		return FsmModeResult{}, errors.New("'useStaticFileContent' must be a boolean value in GetApplicationInfo response.")
	}
	var content *FsmStaticFileContent
	populated := false
	if object, isObject := staticContent.(map[string]any); isObject {
		content = &FsmStaticFileContent{
			SchemasRuntimePath:   runtimeFsmString(object, "schemasRuntimePath"),
			ResourcesRuntimePath: runtimeFsmString(object, "resourcesRuntimePath"),
		}
		populated = runtimeFsmNonBlank(content.SchemasRuntimePath) || runtimeFsmNonBlank(content.ResourcesRuntimePath)
	}
	mode := ""
	switch {
	case !useStaticFileContent && staticContent == nil:
		mode = "on"
	case useStaticFileContent && populated:
		mode = "off"
	default:
		return FsmModeResult{}, errors.New("Could not determine FSM mode from GetApplicationInfo response. " +
			"Expected either useStaticFileContent=false with staticFileContent=null or " +
			"useStaticFileContent=true with staticFileContent populated.")
	}
	return FsmModeResult{Mode: mode, UseStaticFileContent: useStaticFileContent, StaticFileContent: content}, nil
}

// runtimeFsmCollectCandidates walks the whole response, as clio does, and collects every object that carries
// both keys, so a payload nested under applicationInfo or any other wrapper is found.
func runtimeFsmCollectCandidates(value any, candidates *[]map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		_, hasUse := runtimeFsmProperty(typed, "useStaticFileContent")
		_, hasContent := runtimeFsmProperty(typed, "staticFileContent")
		if hasUse && hasContent {
			*candidates = append(*candidates, typed)
		}
		for _, item := range typed {
			runtimeFsmCollectCandidates(item, candidates)
		}
	case []any:
		for _, item := range typed {
			runtimeFsmCollectCandidates(item, candidates)
		}
	}
}

// runtimeFsmProperty matches a key case-insensitively, like clio's TryGetProperty.
func runtimeFsmProperty(object map[string]any, name string) (any, bool) {
	if value, ok := object[name]; ok {
		return value, true
	}
	for key, value := range object {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

// runtimeFsmString reads a path the way clio's case-insensitive deserializer does: a non-string reads as unset.
func runtimeFsmString(object map[string]any, name string) *string {
	value, _ := runtimeFsmProperty(object, name)
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

func runtimeFsmNonBlank(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != ""
}
