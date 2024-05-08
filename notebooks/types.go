package notebooks

import "encoding/json"

type Notebook struct {
	Metadata      Metadata `json:"metadata"`
	NBFormatMinor int      `json:"nbformat_minor"`
	NBFormat      int      `json:"nbformat"`
	Cells         []Cell   `json:"cells"`
}

func (n *Notebook) Validate() {
	n.Metadata.Validate()
	if n.NBFormat == 0 {
		n.NBFormat = 4
	}
	if n.Cells == nil {
		n.Cells = []Cell{}
	}
	for _, cell := range n.Cells {
		cell.Validate()
	}

}

type Metadata struct {
	KernelSpec   *KernelSpec   `json:"kernelspec,omitempty"`
	LanguageInfo *LanguageInfo `json:"language_info,omitempty"`
	OrigNBFormat int           `json:"orig_nbformat,omitempty"`
	Title        string        `json:"title,omitempty"`
	Authors      []Author      `json:"authors,omitempty"`
}

func (m *Metadata) Validate() {
}

type KernelSpec struct {
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type LanguageInfo struct {
	Name           string      `json:"name"`
	CodeMirrorMode interface{} `json:"codemirror_mode,omitempty"`
	FileExtension  string      `json:"file_extension"`
	MimeType       string      `json:"mimetype"`
	PygmentsLexer  string      `json:"pygments_lexer"`
}

type Author struct {
	Name string `json:"name"`
}

type Cell struct {
	ID             string                `json:"id"`
	CellType       string                `json:"cell_type"`
	Metadata       *CellMetadata         `json:"metadata"`
	Source         *MultilineString      `json:"source,omitempty"`
	Attachments    map[string]MimeBundle `json:"attachments,omitempty"`
	Outputs        []Output              `json:"outputs"`
	ExecutionCount *int                  `json:"execution_count"`
}

func (c *Cell) Validate() {
	if c.Metadata != nil {
		c.Metadata.Validate()
	}
	for _, output := range c.Outputs {
		output.Validate()
	}
}

type CellMetadata struct {
	Format    string            `json:"format"`
	Jupyter   Jupyter           `json:"jupyter,omitempty"`
	Name      string            `json:"name"`
	Tags      []string          `json:"tags,omitempty"`
	Execution map[string]string `json:"execution,omitempty"`
	Collapsed bool              `json:"collapsed"`
	Scrolled  interface{}       `json:"scrolled,omitempty"`
}

func (cm *CellMetadata) Validate() {
	if cm.Name == "" {
		cm.Name = "cell"
	}
}

type Jupyter struct {
	SourceHidden  bool `json:"source_hidden"`
	OutputsHidden bool `json:"outputs_hidden"`
}

type Output struct {
	OutputType     string          `json:"output_type"`
	ExecutionCount *int            `json:"execution_count"`
	Data           MimeBundle      `json:"data,omitempty"`
	Metadata       OutputMetadata  `json:"metadata,omitempty"`
	Name           string          `json:"name"`
	Text           MultilineString `json:"text,omitempty"`
	EName          string          `json:"ename"`
	EValue         string          `json:"evalue"`
	Traceback      []string        `json:"traceback,omitempty"`
}

func (o *Output) Validate() {
}

type OutputMetadata map[string]interface{}

type MimeBundle map[string]MultilineString

type MultilineString struct {
	Value string
	Lines []string
}

func (ms *MultilineString) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &ms.Value); err == nil {
		return nil
	}
	return json.Unmarshal(data, &ms.Lines)
}

func (ms MultilineString) MarshalJSON() ([]byte, error) {
	if len(ms.Lines) > 0 {
		return json.Marshal(ms.Lines)
	}
	return json.Marshal(ms.Value)
}
