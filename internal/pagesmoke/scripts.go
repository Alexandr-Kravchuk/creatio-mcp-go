package pagesmoke

import (
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// The Freedom UI DOM contract page-smoke relies on, as the scripts it evaluates in the page
// (FreedomUiPageScripts). The literals are generated from clio's source (scripts_gen.go); they are joined here
// exactly as clio's expressions join them.

const (
	maxText    = 60
	maxButtons = 30
	maxFields  = 40
)

var (
	readyScript     = "(() => {" + pagePrelude + readyBody
	inventoryScript = "(() => {" + pagePrelude +
		fmt.Sprintf("const MAX_TEXT = %d; const MAX_BUTTONS = %d; const MAX_FIELDS = %d;", maxText, maxButtons, maxFields) + inventoryBody
	overlayScript = "(() => {" + pagePrelude + fmt.Sprintf("const MAX = %d;", maxText*5) + overlayBody
)

// clickScript is BuildClickScript: the button's caption or element name goes in as a JSON string literal.
func clickScript(button string) string {
	literal, err := creatio.ClioJSONText(button)
	if err != nil {
		literal = `""`
	}
	return "((wanted) => {" + pagePrelude + fmt.Sprintf("const MAX_TEXT = %d;", maxText) + clickBody + literal + ")"
}

// PageButton is a visible button of the page.
type PageButton struct {
	Name     *string `json:"name"`
	Caption  *string `json:"caption"`
	Disabled bool    `json:"disabled,omitzero"`
}

// PageField is a visible field of the page.
type PageField struct {
	Name     *string `json:"name"`
	Type     *string `json:"type"`
	Caption  *string `json:"caption"`
	Required bool    `json:"required,omitzero"`
	Readonly bool    `json:"readonly,omitzero"`
	Value    *string `json:"value,omitzero"`
	Error    *string `json:"error,omitzero"`
}

type pageReadyState struct {
	State     string  `json:"state"`
	Why       *string `json:"why"`
	Href      *string `json:"href"`
	Signature *string `json:"signature"`
}

type pageInventory struct {
	Title        *string      `json:"title"`
	Scope        *string      `json:"scope"`
	TotalButtons int          `json:"totalButtons"`
	TotalFields  int          `json:"totalFields"`
	Buttons      []PageButton `json:"buttons"`
	Fields       []PageField  `json:"fields"`
}

type pageOverlays struct {
	Dialogs []string `json:"dialogs"`
	Toasts  []string `json:"toasts"`
}

type pageClickOutcome struct {
	Outcome    string       `json:"outcome"`
	Name       *string      `json:"name"`
	Caption    *string      `json:"caption"`
	Candidates []PageButton `json:"candidates"`
}
