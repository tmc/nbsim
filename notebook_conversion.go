package nbsim

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/nbsim/notebooks"
	"golang.org/x/net/html"
)

type Handler struct {
	RootDir           string
	StaticFileHandler http.Handler
	GenFunc           func(*url.URL) (string, error)
}

// handleNotebookConversion handles the conversion of a notebook to html
func NewNotebookConversionHandler(rootDir string, notFoundHandler http.Handler, genNewPath func(*url.URL) (string, error)) *Handler {
	return &Handler{
		RootDir:           rootDir,
		StaticFileHandler: notFoundHandler,
		GenFunc:           genNewPath,
	}
}

func (h *Handler) htmlToIpynbPath(htmlPath string) string {
	return strings.Replace(htmlPath, ".html", ".ipynb", 1)
}

func (h *Handler) resolvePath(path string) string {
	return filepath.Join(h.RootDir, path)
}

func (h *Handler) pathExistsOrWill(htmlPath string) bool {
	// return true unless it's a dir:
	// if starts with gen-, return true:
	if strings.HasPrefix(htmlPath, "/gen-") {
		return true
	}
	si, _ := os.Stat(h.resolvePath(htmlPath))
	if si != nil && si.IsDir() {
		return false
	}
	// return false if it's index.html or /assets/*:
	if htmlPath == "/index.html" || strings.HasPrefix(htmlPath, "/assets/") || strings.HasPrefix(htmlPath, "/favicon.ico") {
		return false
	}
	return true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 404 favicon.ico:
	if r.URL.Path == "/favicon.ico" {
		http.Error(w, "404 favicon.ico", http.StatusNotFound)
		return
	}
	// fmt.Println("pathExistsOrWill?", h.pathExistsOrWill(r.URL.Path), r.URL.Path)
	// if h.pathExistsOrWill(r.URL.Path) {
	fmt.Println("calling serveStreamedNotebookConversion for", r.URL.Path)
	h.serveStreamedNotebookConversion(w, r)
	// } else {
	// 	fmt.Println(" --> serving static file server for", r.URL.Path)
	// 	h.NotFoundHandler.ServeHTTP(w, r)
	// }
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

	r.URL.Host = ""
	r.URL.Scheme = ""

	// url to gen-$(md5).html:
	// if starts with gen-, call GenFunc and return
	if strings.HasPrefix(r.URL.Path, "/gen-") {
		p, err := h.GenFunc(r.URL)
		fmt.Println(r.URL.Path, "GenFunc returned", p, err)
		return
	}

	pathMd5 := fmt.Sprintf("%x", md5.Sum([]byte(r.URL.Path)))
	notebookPath := fmt.Sprintf("/gen-%s.ipynb", pathMd5)
	notebookRawPath := fmt.Sprintf("/gen-%s-raw.ipynb", pathMd5)

	// Flush the response writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	t1 := time.Now()
	for i := 0; i < 1000; i++ {
		time.Sleep(10 * time.Millisecond)
		fmt.Println("loop", i)
		// Read the notebook file
		notebook, err := os.ReadFile(h.resolvePath(notebookRawPath))
		if err != nil {
			if os.IsNotExist(err) && time.Since(t1) < 10*time.Second {
				if i == 0 {
					go func() {
						p, err := h.GenFunc(r.URL)
						fmt.Println(r.URL, "GenFunc returned", p, err)
					}()
				}
				time.Sleep(time.Second)
				continue
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Check if the notebook has finished generating
		notebookJSON, notebookDone = notebooks.RepairNotebookJSON(string(notebook))
		fmt.Fprintln(os.Stderr, "notebookDone", notebookDone, "notebookJSON", notebookJSON)

		// Generate the HTML body
		htmlBody, err := generateNotebookHTML([]byte(notebookJSON))
		if err != nil {
			fmt.Println("error generating notebook HTML:", err)
			continue
		}
		// Get the complete divs
		divs, err := getCompleteDivs(notebookDone, htmlBody, prevDivCount)
		if err != nil {
			fmt.Println("error getting complete divs:", err)
			continue
		}

		if !headerWritten && len(divs) > 0 {
			headerWritten = true
			fmt.Fprint(w, getPreamble(htmlBody))
		}

		// // Write the divs to the response
		for _, div := range divs {
			fmt.Println("writing div for", r.URL.Path, "(done?", notebookDone, ")", len(divs), "divs")
			time.Sleep(650 * time.Millisecond)
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
			if os.IsNotExist(err) {
				continue
			}
			fmt.Println("error statting", notebookPath, err)
		}

		if fileInfo.ModTime().After(lastModTime) {
			lastModTime = fileInfo.ModTime()
		} else {
			// If no update for a certain amount of time, assume notebook is done
			time.Sleep(5 * time.Second)
			if time.Since(lastModTime) > 30*time.Second {
				fmt.Println("notebook done by timeout")
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
	html, err := runNbconvert(tmpFile.Name(), true)
	if err != nil {
		return "", fmt.Errorf("error running nbconvert: %w", err)
	}
	cache[md5] = string(html)
	return string(html), nil
}

// runNbconvert runs nbconvert to convert a notebook to HTML.
func runNbconvert(notebookPath string, debug bool) (string, error) {
	// Run nbconvert to convert the notebook to HTML
	fmt.Println("running nbconvert", notebookPath)
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
	if len(divs) == 0 {
		return nil, fmt.Errorf("no divs found")
	}
	return divs[prevDivCount:], nil
}
