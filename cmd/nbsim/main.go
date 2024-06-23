package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/cors"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/nbsim"
)

var (
	flagServe  = flag.Bool("serve", true, "run in serve mode")
	flagModel  = flag.String("model", "claude-3-5-sonnet-20240620", "model to use")
	flagGenDir = flag.String("gen-dir", "generated", "directory to write generated notebooks to")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	llm, err := anthropic.New(
		anthropic.WithModel(*flagModel),
	)
	if err != nil {
		return err
	}

	if *flagServe {
		return serve(ctx, llm)
	} else {
		return repl(ctx, llm)
	}
}

type Server struct {
	llm              llms.Model
	alreadyGenerated map[string]string
}

func serve(ctx context.Context, llm llms.Model) error {
	ch := cors.AllowAll()

	s := &Server{
		llm:              llm,
		alreadyGenerated: map[string]string{},
	}
	assetsFS, err := nbsim.GetViewerFileAssets()
	if err != nil {
		return err
	}
	assetServer := handleAssetsWithRootFallback(assetsFS)

	//http.HandleFunc("/_gen", s.handleGen)
	http.Handle("/", nbsim.NewNotebookConversionHandler(*flagGenDir, assetServer, s.trickleNotebook))
	fmt.Println("serving on :8080")
	return http.ListenAndServe(":8080", ch.Handler(http.DefaultServeMux))
}

func handleAssetsWithRootFallback(assets fs.FS) http.Handler {
	fs := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fs.ServeHTTP(w, r)
			return
		}
		f, err := assets.Open(strings.TrimPrefix(path.Clean(r.URL.Path), "/"))
		if err == nil {
			defer f.Close()
		}
		if os.IsNotExist(err) {
			r.URL.Path = "/"
		}
		fs.ServeHTTP(w, r)
	})
}

func repl(ctx context.Context, llm llms.Model) error {
	scanner := bufio.NewScanner(os.Stdin)
	history := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, nbsim.SystemPrompt),
	}
	nw := nbsim.NewNotebookWriter(*flagGenDir, "generated")
	defer nw.Finish()
	for {
		ctx, cancelFn := context.WithCancel(ctx)
		fmt.Print("$ ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			cancelFn()
			continue
		}
		history = append(history, llms.TextParts(llms.ChatMessageTypeHuman, input))
		history = append(history, llms.TextParts(llms.ChatMessageTypeAI, "{"))
		_, err := llm.GenerateContent(ctx,
			history,
			llms.WithTemperature(1),
			llms.WithMaxTokens(4096),
			llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
				nw.AddPart(string(chunk))
				return nil
			}),
		)
		cancelFn()
		if err != nil {
			return err
		}
		fmt.Println()
	}
	return nil
}

// func (s *Server) handleGen(w http.ResponseWriter, r *http.Request) {
// 	payload := map[string]any{}
// 	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
// 		fmt.Println("error decoding payload:", err)
// 		http.Error(w, err.Error(), http.StatusBadRequest)
// 		return
// 	}

// 	path, ok := payload["url"].(string)
// 	if !ok {
// 		path = "/notebooks/super-hyped/finetune-llama-7.ipynb"
// 	}

// 	u, _ := url.Parse(path)
// 	nbHTMLPath, err := s.generateNotebook(u)
// 	if err != nil {
// 		http.Error(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	json.NewEncoder(w).Encode(map[string]string{"url": nbHTMLPath})
// }

// isAlreadyGenerated returns true if the key has already been generated.
// If the key is not in the alreadyGenerated map, it will check the filesystem
func (s *Server) isAlreadyGenerated(key string) bool {
	ok := false
	// stat the file to see if it exists:
	si, err := os.Stat(path.Join(*flagGenDir, key+"-final.ipynb"))
	ok = err == nil
	// if the file exists, check if it's old enough to be considered stale:
	if ok {
		if time.Since(si.ModTime()) > 24*time.Hour {
			ok = false
		}
		if si.Size() == 0 {
			ok = false
		}
	}
	return ok
}

func (s *Server) setAlreadyGenerated(key, value string) {
	s.alreadyGenerated[key] = value
}

func (s *Server) trickleNotebook(ctx context.Context, l *url.URL) (chan string, error) {
	// md5 the url to get a unique identifier:
	path := l.String()
	nbBase := fmt.Sprintf("gen-%x", md5.Sum([]byte(l.String())))
	// if we already have an output file, return early, unless it looks old enough and is empty:
	fmt.Println("already generated?", s.isAlreadyGenerated(nbBase))
	if !s.isAlreadyGenerated(nbBase) {
		fmt.Println("opting to generate notebook", nbBase, "for", path)
		return s.generateNotebook(ctx, l)
	}

	nw := nbsim.NewNotebookWriter(*flagGenDir, nbBase)
	nw.TouchOutputFile()
	s.setAlreadyGenerated(nbBase, nbBase)

	fmt.Println("trickling notebook", nbBase, "for", path)
	// we trickle by opening the -final file and writing it to the -raw file:
	fmt.Println(filepath.Join(*flagGenDir, nbBase+"-final.ipynb"))
	f, err := os.ReadFile(filepath.Join(*flagGenDir, nbBase+"-final.ipynb"))
	if err != nil {
		return nil, err
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		// split to fields, add parts with delay:
		parts := strings.Fields(string(f))

		fmt.Println(string(f))
		fmt.Println("trickling", len(parts), "parts")
		for _, part := range parts {
			nw.AddPart(part)
			time.Sleep(10 * time.Millisecond)
			if part, ok := nw.NextPart(); ok {
				ch <- part
			}
		}
	}()
	return ch, nil
}

func (s *Server) generateNotebook(ctx context.Context, l *url.URL) (chan string, error) {
	// md5 the url to get a unique identifier:
	path := l.String()
	nbBase := fmt.Sprintf("gen-%x", md5.Sum([]byte(l.String())))
	nbHTMLPath := fmt.Sprintf("%s.html", nbBase)

	nw := nbsim.NewNotebookWriter(*flagGenDir, nbBase)
	nw.TouchOutputFile()

	s.setAlreadyGenerated(nbBase, nbHTMLPath)
	fmt.Println("generating notebook", nbBase, "for", path)

	history := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, nbsim.SystemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, path),
		llms.TextParts(llms.ChatMessageTypeAI, "{"),
	}
	// open log file for append:
	lf, err := os.OpenFile(filepath.Join(*flagGenDir, nbBase+".claude.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("error opening log file:", err)
	}

	ch := make(chan string)
	go func() {
		defer nw.Finish()
		defer close(ch)
		ctx := context.WithoutCancel(ctx)
		_, err = s.llm.GenerateContent(ctx,
			history,
			llms.WithTemperature(1),
			llms.WithMaxTokens(4096),
			llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
				// append to .claude.log:
				if lf != nil {
					lf.Write(chunk)
				}
				nw.AddPart(string(chunk))
				if part, ok := nw.NextPart(); ok {
					ch <- part
				}
				return nil
			}),
		)
		if err != nil {
			fmt.Println("error generating content:", err)
			ch <- renderClientError(err)
		}
	}()
	if err != nil {
		fmt.Println("error generating content:", err)
		return nil, err
	}
	return ch, nil
}

// renderClientError returns an HTML error message for the client
func renderClientError(err error) string {
	return fmt.Sprintf("<div><h1>Error</h1><p>%s</p></div>", err)
}
