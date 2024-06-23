package nbsim

import (
	"context"
	"embed"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tmc/nbsim/notebooks"
)

//go:embed system-prompt.txt
var SystemPrompt string

//go:embed viewer/dist/*
var ViewerAssets embed.FS

func GetViewerFileAssets() (fs.FS, error) {
	// fs.Sub to the viewer/dist directory:
	s, err := fs.Sub(ViewerAssets, "viewer/dist")
	if err != nil {
		return nil, err
	}
	return s, nil
}

type gen struct {
	contents string
	done     bool
}

var mu = &sync.Mutex{}
var genned map[string]gen

type notebookWriter struct {
	parts       []string
	repaired    string
	baseDir     string
	outfileBase string
	done        bool
}

func NewNotebookWriter(baseDir string, outfileBase string) *notebookWriter {
	return &notebookWriter{
		parts:       []string{"{"},
		baseDir:     baseDir,
		outfileBase: outfileBase,
	}
}

func (nw *notebookWriter) filePath(suffix string) string {
	return filepath.Join(nw.baseDir, fmt.Sprintf("%s%s.ipynb", nw.outfileBase, suffix))
}

func (nw *notebookWriter) AddPart(part string) {
	nw.parts = append(nw.parts, part)
	s := strings.Join(nw.parts, "")
	var ok bool
	nw.repaired, ok = notebooks.RepairNotebookJSON(s)
	if !ok {
		// fmt.Println("issue repairing json:", err)
		// // write to partial.ipynb:
		// os.WriteFile("partial.ipynb", []byte(s), 0644)
	}
	nb := &notebooks.Notebook{}
	os.WriteFile(nw.filePath("-raw"), []byte(strings.Join(nw.parts, "")), 0644)
	if err := json.Unmarshal([]byte(nw.repaired), nb); err != nil {
		fmt.Println("issue unmarshalling json:", err)
		return
	}
	nb.Validate()
	repaired, err := json.MarshalIndent(nb, "", "  ")
	if err != nil {
		fmt.Println("issue marshalling json:", err)
		return
	}
	// write to generated.ipynb then run nbonvert:
	of := nw.filePath("")
	os.WriteFile(of, []byte(repaired), 0644)
}

func (nw *notebookWriter) convert() {
	nb := &notebooks.Notebook{}
	if nw.repaired == "" {
		return
	}
	os.WriteFile(fmt.Sprintf("%s-raw.ipynb", nw.outfileBase), []byte(nw.repaired), 0644)
	if err := json.Unmarshal([]byte(nw.repaired), nb); err != nil {
		fmt.Println("issue unmarshalling json:", err)
		return
	}
	nb.Validate()
	repaired, err := json.MarshalIndent(nb, "", "  ")
	if err != nil {
		fmt.Println("issue marshalling json:", err)
		return
	}
	// write to generated.ipynb then run nbconvert:
	of := fmt.Sprintf("%s.ipynb", nw.outfileBase)
	os.WriteFile(of, []byte(repaired), 0644)
	// run nbconvert to convert the notebook to HTML:
	fmt.Println("running nbconvert on", of)
	cmd := exec.Command("jupyter", "nbconvert", "--to", "html", of)
	cmd.Run()
}

func (nw *notebookWriter) NextPart() (string, bool) {
	if len(nw.parts) == 0 {
		return "", false
	}
	return nw.parts[len(nw.parts)-1], true
	// htmlFile := nw.filePath(fmt.Sprintf("%s.html", nw.outfileBase))
	// htmlContent, err := os.ReadFile(htmlFile)
	// if err != nil {
	// 	fmt.Println("error reading HTML file:", err)
	// 	return "", false
	// }
	// return string(htmlContent), true
}

func (nw *notebookWriter) Finish() error {
	fmt.Println("finishing notebook")
	// write -final version:
	nw.done = true
	nw.convert()
	return os.WriteFile(nw.filePath("-final"), []byte(nw.repaired), 0644)
}

func (nw *notebookWriter) startConverter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			nw.convert()
			return
		case <-time.After(1 * time.Second):
			nw.convert()
		}
	}
}

func (nw *notebookWriter) TouchOutputFile() {
	of := nw.filePath(fmt.Sprintf("%s.ipynb", nw.outfileBase))
	os.WriteFile(of, []byte(nw.repaired), 0644)
	of = nw.filePath(fmt.Sprintf("%s-raw.ipynb", nw.outfileBase))
	os.WriteFile(of, []byte(nw.repaired), 0644)
}
