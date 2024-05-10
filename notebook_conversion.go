package nbsim

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tmc/nbsim/notebooks"
	"golang.org/x/net/html"
)

type Handler struct {
	RootDir           string
	StaticFileHandler http.Handler
	GenFunc           func(context.Context, *url.URL) (chan string, error)
}

func NewNotebookConversionHandler(rootDir string, notFoundHandler http.Handler, genNewPath func(context.Context, *url.URL) (chan string, error)) *Handler {
	return &Handler{
		RootDir:           rootDir,
		StaticFileHandler: notFoundHandler,
		GenFunc:           genNewPath,
	}
}

func (h *Handler) resolvePath(path string) string {
	return filepath.Join(h.RootDir, path)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/favicon.ico" {
		http.Error(w, "404 favicon.ico", http.StatusNotFound)
		return
	}

	fmt.Println("calling serveStreamedNotebookConversion for", r.URL.Path)
	h.serveStreamedNotebook(w, r)
}

func (h *Handler) serveStreamedNotebook(w http.ResponseWriter, r *http.Request) {
	partsCh, err := h.GenFunc(r.Context(), r.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")

	for part := range partsCh {
		fmt.Print(".")
		fmt.Fprint(w, part)
		w.(http.Flusher).Flush()
	}
}

func (h *Handler) serveStreamedNotebookConversion1(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")

	var lastModTime time.Time
	var prevDivCount int
	var notebookDone bool
	var headerWritten bool
	var notebookJSON string

	r.URL.Host = ""
	r.URL.Scheme = ""

	if strings.HasPrefix(r.URL.Path, "/gen-") {
		p, err := h.GenFunc(r.Context(), r.URL)
		fmt.Println(r.URL.Path, "GenFunc returned", p, err)
		return
	}

	pathMd5 := fmt.Sprintf("%x", md5.Sum([]byte(r.URL.Path)))
	notebookPath := fmt.Sprintf("/gen-%s.ipynb", pathMd5)
	notebookRawPath := fmt.Sprintf("/gen-%s-raw.ipynb", pathMd5)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	t1 := time.Now()
	for i := 0; i < 1000; i++ {
		time.Sleep(10 * time.Millisecond)
		fmt.Println("loop", i)

		notebook, err := os.ReadFile(h.resolvePath(notebookRawPath))
		if err != nil {
			if os.IsNotExist(err) && time.Since(t1) < 10*time.Second {
				if i == 0 {
					go func() {
						p, err := h.GenFunc(r.Context(), r.URL)
						fmt.Println(r.URL, "GenFunc returned", p, err)
					}()
				}
				time.Sleep(time.Second)
				continue
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		notebookJSON, notebookDone = notebooks.RepairNotebookJSON(string(notebook))
		fmt.Fprintln(os.Stderr, "notebookDone", notebookDone)

		htmlBody, err := generateNotebookHTML([]byte(notebookJSON))
		if err != nil {
			fmt.Println("error generating notebook HTML:", err)
			continue
		}

		divs, err := getCompleteDivs(notebookDone, htmlBody, prevDivCount)
		if err != nil {
			fmt.Println("error getting complete divs:", err)
			continue
		}

		if !headerWritten && len(divs) > 0 {
			headerWritten = true
			fmt.Fprint(w, getPreamble(htmlBody))
		}

		for _, div := range divs {
			fmt.Println("writing div for", r.URL.Path, "(done?", notebookDone, ")", len(divs), "divs")
			// fmt.Println(div)
			fmt.Fprint(w, div)
			flusher.Flush()
		}

		flusher.Flush()

		prevDivCount += len(divs)

		if notebookDone {
			fmt.Fprint(w, "</main></body></html>")
			break
		}

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
			time.Sleep(5 * time.Second)
			if time.Since(lastModTime) > 30*time.Second {
				fmt.Println("notebook done by timeout")
				notebookDone = true
			}
		}
	}
}

var cacheMu = &sync.Mutex{}
var cache = make(map[string]string)

func generateNotebookHTML(in []byte) (string, error) {
	md5 := fmt.Sprintf("%x", md5.Sum(in))
	if html, ok := cache[md5]; ok {
		return html, nil
	}

	tmpFile, err := os.CreateTemp("", "notebook-*.ipynb")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.Write(in); err != nil {
		return "", err
	}

	html, err := runNbconvert(tmpFile.Name(), false)
	if err != nil {
		return "", fmt.Errorf("error running nbconvert: %w", err)
	}
	cacheMu.Lock()
	cache[md5] = string(html)
	cacheMu.Unlock()
	return string(html), nil
}

func runNbconvert(notebookPath string, debug bool) (string, error) {
	fmt.Println("running nbconvert", notebookPath)
	cmd := exec.Command("jupyter", "nbconvert", "--to", "html", notebookPath)

	if debug {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		return "", err
	}

	htmlPath := strings.Replace(notebookPath, ".ipynb", ".html", 1)
	html, err := os.ReadFile(htmlPath)
	if err != nil {
		return "", err
	}
	return string(html), nil
}

func getPreamble(htmlBody string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(htmlBody))
	var preamble string
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
	return preamble
}

func getCompleteDivs(notebookDone bool, htmlBody string, prevDivCount int) ([]string, error) {
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return nil, err
	}
	var divs []string

	var f func(*html.Node)
	f = func(n *html.Node) {
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

	if !notebookDone {
		if len(divs) > 0 {
			divs = divs[:len(divs)-1]
		}
	}

	if len(divs) == 0 {
		return nil, fmt.Errorf("no divs found")
	}
	return divs[prevDivCount:], nil
}
