package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/rs/cors"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/schema"
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

	http.HandleFunc("/gen", s.handleGen)
	http.Handle("/", nbsim.NewNotebookConversionHandler(*flagGenDir, assetServer))
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
		llms.TextParts(schema.ChatMessageTypeSystem, nbsim.SystemPrompt),
	}
	nw := nbsim.NewNotebookWriter(*flagGenDir, "generated")
	for {
		ctx, cancelFn := context.WithCancel(ctx)
		_ = scanner
		/*
			fmt.Print("$ ")
			if !scanner.Scan() {
				break
			}
			input := strings.TrimSpace(scanner.Text())
			if input == "" {
				continue
			}
		*/
		input := "https://brev.dev/notebooks/super-hyped/finetune-llama-7.ipynb"
		history = append(history, llms.TextParts(schema.ChatMessageTypeHuman, input))
		history = append(history, llms.TextParts(schema.ChatMessageTypeAI, "{"))
		_, err := llm.GenerateContent(ctx,
			history,
			llms.WithTemperature(1),
			llms.WithMaxTokens(4096),
			llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
				//fmt.Print(string(chunk))
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
}

func (s *Server) handleGen(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	nbBase := fmt.Sprintf("gen-%d", time.Now().Unix())
	nbHTMLPath := fmt.Sprintf("%s.html", nbBase)

	nw := nbsim.NewNotebookWriter(*flagGenDir, nbBase)
	if _, ok := s.alreadyGenerated[nbBase]; ok {
		json.NewEncoder(w).Encode(map[string]string{"url": nbHTMLPath})
		return
	}
	s.alreadyGenerated[nbBase] = nbHTMLPath
	fmt.Println("generating notebook", nbBase)

	go func() {
		ctx, cancelFn := context.WithCancel(context.Background())
		defer cancelFn()

		input, ok := payload["url"].(string)
		if !ok {
			input = "/notebooks/super-hyped/finetune-llama-7.ipynb"
		}
		history := []llms.MessageContent{
			llms.TextParts(schema.ChatMessageTypeSystem, nbsim.SystemPrompt),
			llms.TextParts(schema.ChatMessageTypeHuman, input),
			llms.TextParts(schema.ChatMessageTypeAI, "{"),
		}
		_, err := s.llm.GenerateContent(ctx,
			history,
			llms.WithTemperature(1),
			llms.WithMaxTokens(4096),
			llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
				fmt.Print(string(chunk))
				nw.AddPart(string(chunk))
				return nil
			}),
		)
		//nw.outfileBase + ".ipynb"
		if err != nil {
			fmt.Println("error generating content:", err)
			return
		}
	}()
	json.NewEncoder(w).Encode(map[string]string{"url": nbHTMLPath})
}
