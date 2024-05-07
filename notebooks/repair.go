package notebooks

import "encoding/json"

func RepairNotebookJSON(s string) (string, bool) {
	var suffixes = []string{
		``, `}`, `}]}`, `"]}}`, `"]}]}`, `]}]}`, `"]}]}`,
		`""]}]}`, `":null}]}`, `null}]}`,
	}
	var o Notebook
	var ok bool
	var repaired []byte
	for _, suffix := range suffixes {
		if err := json.Unmarshal([]byte(s+suffix), &o); err == nil {
			ok = suffix == ``
			repaired = []byte(s + suffix)
			break
		}
	}
	o.Validate()
	repaired, _ = json.Marshal(o)
	return string(repaired), ok
}
