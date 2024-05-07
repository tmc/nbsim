package nbsim

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/nbsim/notebooks"
	"golang.org/x/net/html"
)

type Handler struct {
	RootDir         string
	NotFoundHandler http.Handler
}

// handleNotebookConversion handles the conversion of a notebook to html
func NewNotebookConversionHandler(rootDir string, notFoundHandler http.Handler) *Handler {
	return &Handler{
		RootDir:         rootDir,
		NotFoundHandler: notFoundHandler,
	}
}

func (h *Handler) htmlToIpynbPath(htmlPath string) string {
	return strings.Replace(htmlPath, ".html", ".ipynb", 1)
}

func (h *Handler) resolvePath(path string) string {
	return filepath.Join(h.RootDir, path)
}

func (h *Handler) pathExistsOrWill(htmlPath string) bool {
	ipynbPath := h.htmlToIpynbPath(htmlPath)
	if s, err := os.Stat(h.resolvePath(htmlPath)); err == nil {
		return !s.IsDir()
	}
	if s, err := os.Stat(h.resolvePath(ipynbPath)); err == nil {
		return !s.IsDir()
	}
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.pathExistsOrWill(r.URL.Path) {
		fmt.Println("calling serveStreamedNotebookConversion")
		h.serveStreamedNotebookConversion(w, r)
	} else {
		fmt.Println("calling not found handler")
		h.NotFoundHandler.ServeHTTP(w, r)
	}
}

// serveStreamedNotebookConversion serves the conversion of a notebook to html.
// The algorithm is as follows:
// 1. Check if the notebook has already finished generating. We know this by seeing if it parses successfully.
// 2. If the notebook is not done generating, we need to stream the divs of the html body only if they are complete. if the notebook is not done we should only consider a cell complete if it has a subsequent cell.
// 3. If the notebook is done generating, we can serve the remaining cells and the end of the html body.
// 4. We periodically poll the input ipynb file to see if it has been updated. If it has, we determine the additional divs to send to the client.
// 5. If we do not see an update to the ipynb file for a certain amount of time, we can assume the notebook is done generating and we can serve the remaining divs.
func (h *Handler) serveStreamedNotebookConversion(w http.ResponseWriter, r *http.Request) {
	// Set the response headers for streaming
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")

	// Initialize variables
	var lastModTime time.Time
	var prevDivCount int
	var notebookDone bool
	var headerWritten bool
	var notebookJSON string

	// Derive the notebookPath from the URL (replace html with ipynb)
	notebookPath := "." + strings.Replace(r.URL.Path, ".html", ".ipynb", 1)

	// Flush the response writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	for {
		fmt.Println("loop")
		// Read the notebook file
		notebook, err := os.ReadFile(h.resolvePath(notebookPath))
		if err != nil {
			fmt.Fprintln(w, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Check if the notebook has finished generating
		notebookJSON, notebookDone = notebooks.RepairNotebookJSON(string(notebook))
		fmt.Fprintln(os.Stderr, "notebookDone", notebookDone)

		// Generate the HTML body
		htmlBody, err := generateNotebookHTML([]byte(notebookJSON))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Get the complete divs
		divs, err := getCompleteDivs(notebookDone, htmlBody, prevDivCount)
		if err != nil {
			fmt.Fprintln(w, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if !headerWritten && len(divs) > 0 {
			headerWritten = true
			fmt.Println("writing preamble")
			time.Sleep(1 * time.Second)
			fmt.Fprint(w, getPreamble(htmlBody))
		}

		// // Write the divs to the response
		for _, div := range divs {
			fmt.Println("writing div")
			time.Sleep(1 * time.Second)
			fmt.Println(div)
			fmt.Fprint(w, div)
			flusher.Flush()
		}

		// Flush the response
		flusher.Flush()

		// Update the previous div count
		prevDivCount += len(divs)

		// Check if the notebook is done generating
		if notebookDone {
			// Serve the remaining divs and end the HTML body
			fmt.Fprint(w, "</main></body></html>")
			break
		}

		// Check if the notebook file has been modified
		fileInfo, err := os.Stat(notebookPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if fileInfo.ModTime().After(lastModTime) {
			lastModTime = fileInfo.ModTime()
		} else {
			// If no update for a certain amount of time, assume notebook is done
			time.Sleep(5 * time.Second)
			if time.Since(lastModTime) > 30*time.Second {
				notebookDone = true
			}
		}
	}
}

func notebookParses(in []byte) bool {
	nb := notebooks.Notebook{}
	return json.Unmarshal(in, &nb) == nil
}

// map of input md5 to output html
var cache = make(map[string]string)

// generateNotebookHTML generates the HTML representation of a notebook.
// it invokes jupyter nbconvert to convert the notebook to HTML.
func generateNotebookHTML(in []byte) (string, error) {
	// Check if the notebook has already been converted
	md5 := fmt.Sprintf("%x", md5.Sum(in))
	if html, ok := cache[md5]; ok {
		return html, nil
	}

	// Write the notebook to a temporary file
	tmpFile, err := os.CreateTemp("", "notebook-*.ipynb")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.Write(in); err != nil {
		return "", err
	}
	// Run nbconvert to convert the notebook to HTML
	html, err := runNbconvert(tmpFile.Name(), false)
	if err != nil {
		return "", err
	}
	cache[md5] = string(html)
	return string(html), nil
}

// runNbconvert runs nbconvert to convert a notebook to HTML.
func runNbconvert(notebookPath string, debug bool) (string, error) {
	// Run nbconvert to convert the notebook to HTML
	cmd := exec.Command("jupyter", "nbconvert", "--to", "html", notebookPath)

	if debug {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		return "", err
	}

	// Read the generated HTML file
	htmlPath := strings.Replace(notebookPath, ".ipynb", ".html", 1)
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		return "", err
	}
	return string(html), nil
}

// getPreamble gets the preamble of the HTML body -- everything before the first div.
func getPreamble(htmlBody string) string {
	// Create an HTML tokenizer
	tokenizer := html.NewTokenizer(strings.NewReader(htmlBody))
	// Variable to store the preamble (before the first div)
	var preamble string
	// Iterate over the HTML tokens
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			break
		}

		token := tokenizer.Token()
		if token.Type == html.StartTagToken && token.Data == "div" {
			break
		}
		preamble += token.String()
	}
	// Return the preamble
	return preamble
}

// getCompleteDivs gets all the div elements that are complete.
// we do this by parsing the HTML body and returning all divs, except the last one if the notebook is not done.
func getCompleteDivs(notebookDone bool, htmlBody string, prevDivCount int) ([]string, error) {
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return nil, err
	}
	var divs []string

	var f func(*html.Node)
	f = func(n *html.Node) {
		// algo: walk the tree, if we see a div, render it to a buffer and append to divs (don't recurse into it)
		// if we see a div, render it to a buffer and append to divs
		if n.Type == html.ElementNode && n.Data == "div" {
			buf := new(bytes.Buffer)
			html.Render(buf, n)
			divs = append(divs, buf.String())
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}

	}
	f(doc)

	// If the notebook is not done, return all divs except the last one
	if !notebookDone {
		if len(divs) > 0 {
			divs = divs[:len(divs)-1]
		}
	}
	// Return the divs
	return divs[prevDivCount:], nil
}

// // walkNodes walks the nodes of a notebook and generates the HTML representation.
// func walkNodes(notebook []byte, htmlBody string) {
// 	// Create an HTML tokenizer
// 	tokenizer := html.NewTokenizer(strings.NewReader(htmlContent))
// 	// Variable to store the last div element
// 	var lastDiv *html.Token
// 	// Iterate over the HTML tokens
// 	for {
// 		tokenType := tokenizer.Next()
// 		if tokenType == html.ErrorToken {
// 			break
// 		}

// 		token := tokenizer.Token()

// 		// Check if the token is a div element
// 		if token.Type == html.StartTagToken && token.Data == "div" {
// 			lastDiv = &token
// 		}

// 		// Write the token to the response writer if it's not the last div or closing tags
// 		if token.Type != html.EndTagToken || (token.Data != "body" && token.Data != "html") {
// 			if lastDiv == nil || token != *lastDiv {
// 				fmt.Fprintf(w, "%v", token)
// 			}
// 		}
// 	}
// }
