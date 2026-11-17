package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yixian-huang/imgli/internal/cliupload"
)

var importExts = map[string]struct{}{
	".jpg": {}, ".jpeg": {}, ".png": {}, ".gif": {}, ".webp": {}, ".bmp": {},
	".heic": {}, ".heif": {},
}

func runImportDir(args []string) error {
	fsFlag := flag.NewFlagSet("import-dir", flag.ContinueOnError)
	fsFlag.SetOutput(os.Stderr)
	baseURL := fsFlag.String("base-url", envOr("IMGLI_BASE_URL", ""), "图床 base URL（IMGLI_BASE_URL）")
	token := fsFlag.String("token", envOr("IMGLI_TOKEN", ""), "API Token（IMGLI_TOKEN）")
	recursive := fsFlag.Bool("recursive", true, "递归子目录")
	visibility := fsFlag.String("visibility", "public", "public|private")
	dryRun := fsFlag.Bool("dry-run", false, "只列出将上传的文件")
	continueOnErr := fsFlag.Bool("continue", true, "单个失败后继续")
	chunked := fsFlag.String("chunked", "auto", "分片上传: auto | always | never（auto 默认 >=32MiB 分片）")
	fsFlag.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: imgli import-dir [flags] <directory>")
		fmt.Fprintln(os.Stderr, "  批量上传目录内图片（复用 /api/v1/upload；秒传靠服务端 content-hash）。")
		fmt.Fprintln(os.Stderr, "  环境变量: IMGLI_BASE_URL, IMGLI_TOKEN")
		fsFlag.PrintDefaults()
	}
	if err := fsFlag.Parse(args); err != nil {
		return err
	}
	if fsFlag.NArg() != 1 {
		fsFlag.Usage()
		return fmt.Errorf("需要恰好一个目录路径")
	}
	root := fsFlag.Arg(0)
	st, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("不是目录: %s", root)
	}
	vis := strings.TrimSpace(*visibility)
	if vis != "public" && vis != "private" {
		return fmt.Errorf("-visibility 须为 public 或 private")
	}
	chunkMode := strings.ToLower(strings.TrimSpace(*chunked))
	switch chunkMode {
	case "auto", "always", "never":
	default:
		return fmt.Errorf("-chunked 须为 auto、always 或 never")
	}

	var files []string
	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && !*recursive {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if _, ok := importExts[ext]; !ok {
			return nil
		}
		files = append(files, path)
		return nil
	}
	if err := filepath.WalkDir(root, walkFn); err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Println("未发现可导入图片")
		return nil
	}
	fmt.Fprintf(os.Stderr, "将处理 %d 个文件\n", len(files))
	if *dryRun {
		for _, f := range files {
			fmt.Println(f)
		}
		return nil
	}

	base, err := cliupload.NormalizeBaseURL(*baseURL)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	// 接信号：Ctrl-C 让当前上传的 ctx 取消（UploadChunked 会 DELETE 会话），
	// 并阻止 -continue 默认 true 在取消后继续空跑剩余文件。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	okN, failN := 0, 0
	for i, path := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, uerr := cliupload.UploadFile(ctx, cliupload.Opts{
			BaseURL:    base,
			Token:      *token,
			Filename:   filepath.Base(path),
			Visibility: vis,
			Client:     client,
			Chunked:    chunkMode,
			Resume:     true,
		}, path)
		if uerr != nil {
			failN++
			fmt.Fprintf(os.Stderr, "[%d/%d] FAIL %s: %v\n", i+1, len(files), path, uerr)
			if !*continueOnErr {
				return uerr
			}
			continue
		}
		okN++
		fmt.Printf("[%d/%d] OK %s → %s\n", i+1, len(files), path, res.Links.URL)
	}
	fmt.Fprintf(os.Stderr, "完成: ok=%d fail=%d\n", okN, failN)
	if failN > 0 {
		return fmt.Errorf("部分失败: %d", failN)
	}
	return nil
}
