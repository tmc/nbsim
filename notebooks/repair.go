package notebooks

import (
	"encoding/json"
	"fmt"
)

// RepairNotebookJSON attempts to repair a JSON string representing a notebook.
func RepairNotebookJSON(s string) (string, bool) {
	var suffixes = []string{
		``, `}`, `}]}`, `"]}}`, `"]}]}`, `]}]}`, `"]}]}`,
		`""]}]}`, `":null}]}`, `null}]}`,
	}
	var o Notebook
	var noSuffixNeeded bool
	var repaired []byte
	var err error
	for _, suffix := range suffixes {
		if err := json.Unmarshal([]byte(s+suffix), &o); err == nil {
			noSuffixNeeded = suffix == ``
			repaired = []byte(s + suffix)
			// if noSuffixNeeded {
			// 	fmt.Println("No suffix needed, got original:", s)
			// }
			break
		}
	}

	o.Validate()
	repaired, err = json.Marshal(o)
	if err != nil {
		fmt.Println("Error:", err)
		return s, false
	}
	return string(repaired), noSuffixNeeded && len(o.Cells) > 0
}
