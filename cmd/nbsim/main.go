package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rs/cors"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/nbsim"
)

var (
	flagServe  = flag.Bool("serve", true, "run in serve mode")
	flagModel  = flag.String("model", "claude-3-opus-20240229", "model to use")
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
	http.Handle("/", nbsim.NewNotebookConversionHandler(*flagGenDir, assetServer, s.generateNotebook))
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

func (s *Server) handleGen(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		fmt.Println("error decoding payload:", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	path, ok := payload["url"].(string)
	if !ok {
		path = "/notebooks/super-hyped/finetune-llama-7.ipynb"
	}

	u, _ := url.Parse(path)
	nbHTMLPath, err := s.generateNotebook(u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"url": nbHTMLPath})
}

func (s *Server) isAlreadyGenerated(key string) bool {
	_, ok := s.alreadyGenerated[key]
	if !ok {
		// stat the file to see if it exists:
		_, err := os.Stat(path.Join(*flagGenDir, key+".ipynb"))
		ok = err == nil
	}
	return ok
}

func (s *Server) setAlreadyGenerated(key, value string) {
	s.alreadyGenerated[key] = value
}

func (s *Server) generateNotebook(l *url.URL) (string, error) {
	// md5 the url to get a unique identifier:
	path := l.String()
	nbBase := fmt.Sprintf("gen-%x", md5.Sum([]byte(l.String())))
	nbHTMLPath := fmt.Sprintf("%s.html", nbBase)

	// if we already have an output file, return early, unless it looks old enough and is empty:
	if s.isAlreadyGenerated(nbBase) {
		fi, err := os.Stat(filepath.Join(*flagGenDir, nbHTMLPath))
		if err == nil && fi.Size() > 0 {
			return nbHTMLPath, nil
		}
	}

	nw := nbsim.NewNotebookWriter(*flagGenDir, nbBase)
	nw.TouchOutputFile()

	if s.isAlreadyGenerated(nbBase) {
		return nbHTMLPath, nil
	}

	s.setAlreadyGenerated(nbBase, nbHTMLPath)
	fmt.Println("generating notebook", nbBase, "for", path)

	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

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
			return nil
		}),
	)
	if err != nil {
		fmt.Println("error generating content:", err)
		return "", err
	}

	return nbHTMLPath, nil
}
