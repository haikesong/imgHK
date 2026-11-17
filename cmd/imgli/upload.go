package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yixian-huang/imgli/internal/cliupload"
)

func runUpload(args []string) error {
	return runUploadWithStdin(args, os.Stdin)
}

func runUploadWithStdin(args []string, stdin io.Reader) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("base-url", envOr("IMGLI_BASE_URL", ""), "图床 base URL（也可用 IMGLI_BASE_URL）")
	token := fs.String("token", envOr("IMGLI_TOKEN", ""), "API Token（也可用 IMGLI_TOKEN；游客上传可空）")
	format := fs.String("format", "url", "输出格式: url | markdown | json")
	visibility := fs.String("visibility", "", "可选 public|private")
	expiresIn := fs.Int("expires-in", 0, "可选有效期秒数，0=永久/省略")
	name := fs.String("name", "", "stdin 上传时的文件名（默认 stdin.png）")
	chunked := fs.String("chunked", "auto", "分片上传: auto | always | never（auto 默认 >=32MiB 分片）")
	verbose := fs.Bool("verbose", false, "打印用户组有效期/次数限制（quota 或 guest config）")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: imgli upload [flags] <file|->")
		fmt.Fprintln(os.Stderr, "  将本地文件或 stdin 上传到图床 API（POST /api/v1/upload）。")
		fmt.Fprintln(os.Stderr, "  环境变量: IMGLI_BASE_URL, IMGLI_TOKEN")
		fmt.Fprintln(os.Stderr, "  组策略见 docs/user-groups-lifecycle.md；-verbose 打印当前账号上限。")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("需要恰好一个路径参数（文件或 - 表示 stdin）")
	}
	if err := cliupload.ValidateFormat(*format); err != nil {
		return err
	}
	vis := strings.TrimSpace(*visibility)
	if vis != "" && vis != "public" && vis != "private" {
		return fmt.Errorf("-visibility 须为 public 或 private")
	}

	pathArg := fs.Arg(0)
	chunkMode := strings.ToLower(strings.TrimSpace(*chunked))
	switch chunkMode {
	case "auto", "always", "never":
	default:
		return fmt.Errorf("-chunked 须为 auto、always 或 never")
	}
	if pathArg == "-" && chunkMode == "always" {
		return fmt.Errorf("stdin 不支持分片上传；-chunked always 需要文件路径")
	}

	// 必须接信号：Ctrl-C 时让 ctx 传播到 UploadChunked，才能执行 deferred
	// DELETE 清理服务端会话，而不是把进程直接杀掉留下未完成会话。
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// 分片上传可能持续很久；单请求仍受 Cloudflare 100 秒限制。
	ctx, cancel := context.WithTimeout(signalCtx, 30*time.Minute)
	defer cancel()

	if *verbose {
		if err := cliupload.PrintAccessLimits(ctx, *baseURL, *token); err != nil {
			fmt.Fprintf(os.Stderr, "verbose: 读取组限制失败: %v\n", err)
		}
		if *expiresIn > 0 {
			fmt.Fprintf(os.Stderr, "verbose: 本次 expires-in=%d\n", *expiresIn)
		} else {
			fmt.Fprintln(os.Stderr, "verbose: 本次未传 expires-in（永久或由服务端组策略补默认）")
		}
	}

	opts := cliupload.Opts{
		BaseURL:    *baseURL,
		Token:      *token,
		Visibility: vis,
		ExpiresIn:  *expiresIn,
		Chunked:    chunkMode,
		Resume:     true,
	}
	var (
		res *cliupload.Result
		err error
	)
	if pathArg == "-" {
		opts.Filename = strings.TrimSpace(*name)
		if opts.Filename == "" {
			opts.Filename = "stdin.png"
		}
		res, err = cliupload.Upload(ctx, opts, stdin)
	} else {
		opts.Filename = filepath.Base(pathArg)
		if n := strings.TrimSpace(*name); n != "" {
			opts.Filename = n
		}
		res, err = cliupload.UploadFile(ctx, opts, pathArg)
	}
	if err != nil {
		return err
	}
	out, err := cliupload.FormatOutput(*format, res)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
